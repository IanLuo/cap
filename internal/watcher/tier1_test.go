package watcher

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/IanLuo/cap/internal/agents"
	"github.com/IanLuo/cap/internal/board"
	"github.com/IanLuo/cap/internal/state"
)

type fakeHarness struct {
	panes     []agents.Pane
	listErr   error
	closeErr  error
	promptErr error
	notifyErr error
	prompts   []string
	closed    []string
	notified  []string
}

func (f *fakeHarness) List(context.Context) ([]agents.Pane, error) { return f.panes, f.listErr }

func (f *fakeHarness) Prompt(_ context.Context, target, text string) error {
	f.prompts = append(f.prompts, target+": "+text)
	return f.promptErr
}

// Close records the call and, like herdr, drops the pane from the next listing.
func (f *fakeHarness) Close(_ context.Context, pane string) error {
	f.closed = append(f.closed, pane)
	if f.closeErr == nil {
		keep := f.panes[:0]
		for _, p := range f.panes {
			if p.ID != pane {
				keep = append(keep, p)
			}
		}
		f.panes = keep
	}
	return f.closeErr
}

func (f *fakeHarness) Notify(_ context.Context, title, body string) error {
	f.notified = append(f.notified, title+" | "+body)
	return f.notifyErr
}

type fakeReaper struct {
	calls []string
	err   error
}

func (f *fakeReaper) Reap(_ context.Context, project string) error {
	f.calls = append(f.calls, project)
	return f.err
}

func tierWatcher(b board.Reader, s state.Store, h agents.Harness, r board.Reaper, c *clock) *Watcher {
	w := testWatcher(b, s, c)
	w.Agents = h
	w.Reap = r
	w.CloseGrace = time.Minute
	return w
}

// claimedTask is one fs task a launcher holds, with the pane fs started. fs
// reads a lapsed claim as an open task that still names its session, so the
// hold state follows the lease.
func claimedTask(id, project, pane string, round int, leaseLive bool) board.Task {
	hold := "open"
	var exp *time.Time
	if leaseLive {
		hold = "claimed"
		v, _ := time.Parse(time.RFC3339, "2026-11-15T10:05:00Z")
		exp = &v
	}
	return board.Task{
		Task: id, Project: project, Kind: "dev", State: hold,
		Session: "s-1", Round: round, LeaseLive: leaseLive, LeaseExpires: exp,
		Process: board.Process{Status: "working", Pane: pane},
	}
}

func pane(id, agent, status string) agents.Pane {
	return agents.Pane{ID: id, Agent: agent, Status: status, CWD: "/tmp/work"}
}

func actionsIn(t *testing.T, s state.Store, scope string) []state.Action {
	t.Helper()
	rows, err := s.Recent(scope, 1000)
	if err != nil {
		t.Fatalf("read %s: %v", scope, err)
	}
	as, err := state.Actions(rows)
	if err != nil {
		t.Fatalf("decode actions: %v", err)
	}
	return as
}

// seedNudge writes a finished nudge for the task's round, as an earlier tick
// would have left it.
func seedNudge(t *testing.T, s state.Store, task string, round int, at time.Time) {
	t.Helper()
	text, err := state.Encode(state.Action{
		Class: ClassNudge, Target: task, Project: "cap", Task: task, Round: round,
		Trigger: "seeded", At: at, Outcome: state.ActionDone,
	})
	if err != nil {
		t.Fatalf("encode nudge: %v", err)
	}
	if _, err := s.Put(state.KindAction, state.Scope("cap"), "nudge "+task, text); err != nil {
		t.Fatalf("seed nudge: %v", err)
	}
}

// seedCursor writes the cursor a previous tick would have left: the open board
// and the harness snapshot it saw. The next tick compares against it, so no
// baseline reap runs and no earlier action closes the gate.
func seedCursor(t *testing.T, s state.Store, at time.Time, tasks []board.Task, panes []agents.Pane) {
	t.Helper()
	text, err := state.Encode(state.Cursor{At: at, Tasks: snapshot(tasks), Panes: paneSnapshot(panes, nil, at)})
	if err != nil {
		t.Fatalf("encode cursor: %v", err)
	}
	if _, err := s.Put(state.KindCursor, state.CursorScope, "board cursor", text); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}
}

func questionsIn(t *testing.T, s state.Store, scope string) []state.Question {
	t.Helper()
	rows, err := s.Recent(scope, 1000)
	if err != nil {
		t.Fatalf("read %s: %v", scope, err)
	}
	var out []state.Question
	for _, r := range rows {
		if r.Kind != state.KindQuestion || r.Closed {
			continue
		}
		q, err := state.DecodeQuestion(r)
		if err != nil {
			t.Fatalf("decode question: %v", err)
		}
		out = append(out, q)
	}
	return out
}

func tick(t *testing.T, w *Watcher, c *clock, step time.Duration) Report {
	t.Helper()
	c.Advance(step)
	r, err := w.Tick(context.Background())
	if err != nil {
		t.Fatalf("tick at %s: %v", c.at.Format(time.RFC3339), err)
	}
	return r
}

// AC14: a closed round with its fs pane still open yields exactly one fs reap,
// and cap never closes that pane. An unchanged tick acts not at all.
func TestAC14TierOneActsOnlyOnTransitions(t *testing.T) {
	b := &fakeBoard{tasks: []board.Task{claimedTask("t-1", "cap", "p1", 1, true)}}
	s, _ := newStorePath(t)
	c := newClock("2026-11-15T10:00:00Z")
	h := &fakeHarness{panes: []agents.Pane{pane("p1", "claude", "working")}}
	rp := &fakeReaper{}
	w := tierWatcher(b, s, h, rp, c)
	seedCursor(t, s, c.at, b.tasks, h.panes) // the previous tick saw the round open

	b.tasks = nil // the round closed; the pane fs started is still open
	report := tick(t, w, c, 30*time.Second)
	if len(rp.calls) != 1 || rp.calls[0] != "cap" {
		t.Fatalf("reap calls = %v, want exactly [cap]", rp.calls)
	}
	if len(h.closed) != 0 {
		t.Fatalf("cap closed %v; an fs pane is reaped by fs, never closed by cap", h.closed)
	}
	if len(report.Actions) != 1 || report.Actions[0].Class != ClassReap || report.Actions[0].Outcome != state.ActionDone {
		t.Fatalf("actions = %+v, want one done reap", report.Actions)
	}

	quiet := tick(t, w, c, 30*time.Second)
	if len(rp.calls) != 1 {
		t.Fatalf("unchanged tick reaped again: %v", rp.calls)
	}
	if len(quiet.Actions) != 0 || len(quiet.Refused) != 0 {
		t.Fatalf("unchanged tick acted: actions %+v, refused %v", quiet.Actions, quiet.Refused)
	}
}

// AC14, upgrade case: a cursor written before cap kept pane snapshots still
// reaps a round that closes on the first tick with panes; only the pane
// transitions wait for a baseline.
func TestAC14ReapsOnTheBaselineTick(t *testing.T) {
	b := &fakeBoard{tasks: []board.Task{claimedTask("t-1", "cap", "p1", 1, true)}}
	s, _ := newStorePath(t)
	c := newClock("2026-11-15T10:00:00Z")
	h := &fakeHarness{} // the harness listed no panes: the cursor keeps none
	rp := &fakeReaper{}
	w := tierWatcher(b, s, h, rp, c)

	if _, err := w.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}

	h.panes = []agents.Pane{pane("p1", "claude", "idle")}
	b.tasks = nil // the round closed; fs's pane is still open
	report := tick(t, w, c, 30*time.Second)
	if len(rp.calls) != 1 || rp.calls[0] != "cap" {
		t.Fatalf("reap calls = %v, want exactly [cap] on the baseline tick", rp.calls)
	}
	if len(h.prompts) != 0 || len(h.closed) != 0 {
		t.Fatalf("baseline tick prompted %v or closed %v; only the reap runs", h.prompts, h.closed)
	}
	if len(report.Actions) != 1 || report.Actions[0].Class != ClassReap {
		t.Fatalf("actions = %+v, want one reap", report.Actions)
	}
}

// AC14, round ended without the task closing: a lapsed lease reaps once the
// round's nudges are spent, and a fresh review verdict reaps at once. A pane
// cap just spoke to is not reaped in that tick.
func TestAC14ReapsARoundThatEndedOnTheBoard(t *testing.T) {
	t.Run("lease lapsed, nudges spent", func(t *testing.T) {
		b := &fakeBoard{tasks: []board.Task{claimedTask("t-1", "cap", "p1", 1, true)}}
		s, _ := newStorePath(t)
		c := newClock("2026-11-15T10:00:00Z")
		h := &fakeHarness{panes: []agents.Pane{pane("p1", "claude", "idle")}}
		rp := &fakeReaper{}
		w := tierWatcher(b, s, h, rp, c)

		for i := 0; i < maxNudgesPerRound; i++ {
			seedNudge(t, s, "t-1", 1, c.at.Add(-time.Hour))
		}
		seedCursor(t, s, c.at, b.tasks, h.panes)
		b.tasks = []board.Task{claimedTask("t-1", "cap", "p1", 1, false)}
		report := tick(t, w, c, 30*time.Second)

		if len(h.prompts) != 0 {
			t.Fatalf("prompts = %d; the round's nudges are spent", len(h.prompts))
		}
		if len(rp.calls) != 1 || rp.calls[0] != "cap" {
			t.Fatalf("reap calls = %v, want exactly [cap]", rp.calls)
		}
		if len(h.closed) != 0 {
			t.Fatalf("cap closed %v; only fs closes an fs-launched pane", h.closed)
		}
		var reaped bool
		for _, a := range report.Actions {
			if a.Class == ClassReap {
				reaped = true
			}
		}
		if !reaped {
			t.Fatalf("actions = %+v, want a reap", report.Actions)
		}

		quiet := tick(t, w, c, 30*time.Second)
		if len(rp.calls) != 1 {
			t.Fatalf("reap calls = %v after the transition passed; a reap is not repeated", rp.calls)
		}
		if len(quiet.Actions) != 0 {
			t.Fatalf("actions = %+v on an unchanged tick", quiet.Actions)
		}
	})

	t.Run("lease lapsed, a nudge is due", func(t *testing.T) {
		b := &fakeBoard{tasks: []board.Task{claimedTask("t-1", "cap", "p1", 1, true)}}
		s, _ := newStorePath(t)
		c := newClock("2026-11-15T10:00:00Z")
		h := &fakeHarness{panes: []agents.Pane{pane("p1", "claude", "idle")}}
		rp := &fakeReaper{}
		w := tierWatcher(b, s, h, rp, c)

		seedCursor(t, s, c.at, b.tasks, h.panes)
		b.tasks = []board.Task{claimedTask("t-1", "cap", "p1", 1, false)}
		tick(t, w, c, 30*time.Second)

		if len(h.prompts) != 1 {
			t.Fatalf("prompts = %d, want the nudge", len(h.prompts))
		}
		if len(rp.calls) != 0 {
			t.Fatalf("reap calls = %v; the pane cap just nudged is not reaped in the same tick", rp.calls)
		}
	})

	t.Run("review verdict", func(t *testing.T) {
		verdictAt, _ := time.Parse(time.RFC3339, "2026-11-15T10:00:30Z")
		failed := claimedTask("t-2", "cap", "p2", 1, true)
		failed.Kind = "review"
		failed.Agent = board.Observed{Role: "review", State: "fail", Reported: "fail", At: &verdictAt}
		b := &fakeBoard{tasks: []board.Task{failed}}
		s, _ := newStorePath(t)
		c := newClock("2026-11-15T10:00:00Z")
		h := &fakeHarness{panes: []agents.Pane{pane("p2", "claude", "idle")}}
		rp := &fakeReaper{}
		w := tierWatcher(b, s, h, rp, c)

		seedCursor(t, s, c.at, b.tasks, h.panes) // the verdict was already there
		if _, err := w.Tick(context.Background()); err != nil {
			t.Fatalf("first tick: %v", err)
		}
		if len(rp.calls) != 0 {
			t.Fatalf("reap calls = %v; the verdict was already there in the cursor", rp.calls)
		}
		verdictAt = verdictAt.Add(time.Minute)
		failed.Agent.At = &verdictAt
		b.tasks = []board.Task{failed}
		tick(t, w, c, 30*time.Second)

		if len(rp.calls) != 1 || rp.calls[0] != "cap" {
			t.Fatalf("reap calls = %v, want exactly [cap] on the new verdict", rp.calls)
		}
	})
}

// AC15: a second transition inside the gate runs nothing, and the refusal names
// the earlier action.
func TestAC15OneActionPerClassInGate(t *testing.T) {
	b := &fakeBoard{tasks: []board.Task{claimedTask("t-1", "cap", "p1", 1, true)}}
	s, _ := newStorePath(t)
	c := newClock("2026-11-15T10:00:00Z")
	h := &fakeHarness{panes: []agents.Pane{pane("p1", "claude", "idle")}}
	w := tierWatcher(b, s, h, &fakeReaper{}, c)

	seedCursor(t, s, c.at, b.tasks, h.panes)

	b.tasks = []board.Task{claimedTask("t-1", "cap", "p1", 1, false)}
	first := tick(t, w, c, 30*time.Second)
	if len(h.prompts) != 1 {
		t.Fatalf("prompts = %d, want one nudge on the lapsed lease", len(h.prompts))
	}
	nudges := 0
	for _, a := range first.Actions {
		if a.Class == ClassNudge {
			nudges++
		}
	}
	if nudges != 1 {
		t.Fatalf("actions = %+v, want one nudge", first.Actions)
	}

	h.panes = []agents.Pane{pane("p1", "claude", "working")}
	tick(t, w, c, 30*time.Second)
	h.panes = []agents.Pane{pane("p1", "claude", "idle")}
	second := tick(t, w, c, 30*time.Second)

	if len(h.prompts) != 1 {
		t.Fatalf("prompts = %d, want the gated transition to prompt nothing", len(h.prompts))
	}
	if len(second.Actions) != 0 {
		t.Fatalf("actions = %+v, want none inside the gate", second.Actions)
	}
	if len(second.Refused) != 1 || !strings.Contains(second.Refused[0], "earlier action at") {
		t.Fatalf("refused = %v, want one refusal naming the earlier action", second.Refused)
	}
}

// AC16: a claimed task with a lapsed lease and an idle agent is nudged with its
// goal and checks, at most twice per round; a working agent is never prompted.
func TestAC16NudgeIsBounded(t *testing.T) {
	t.Run("twice per round", func(t *testing.T) {
		b := &fakeBoard{tasks: []board.Task{claimedTask("t-1", "cap", "p1", 1, true)}}
		b.tasks[0].Goal = "widen the loop"
		b.tasks[0].Checks = []string{"go test ./..."}
		s, _ := newStorePath(t)
		c := newClock("2026-11-15T10:00:00Z")
		h := &fakeHarness{panes: []agents.Pane{pane("p1", "claude", "idle")}}
		w := tierWatcher(b, s, h, &fakeReaper{}, c)

		if _, err := w.Tick(context.Background()); err != nil {
			t.Fatalf("first tick: %v", err)
		}
		b.tasks = []board.Task{claimedTask("t-1", "cap", "p1", 1, false)}
		b.tasks[0].Goal = "widen the loop"
		b.tasks[0].Checks = []string{"go test ./..."}
		tick(t, w, c, 30*time.Second) // nudge 1

		h.panes = []agents.Pane{pane("p1", "claude", "working")}
		tick(t, w, c, 30*time.Second)
		h.panes = []agents.Pane{pane("p1", "claude", "idle")}
		tick(t, w, c, 120*time.Second) // nudge 2

		h.panes = []agents.Pane{pane("p1", "claude", "working")}
		tick(t, w, c, 30*time.Second)
		h.panes = []agents.Pane{pane("p1", "claude", "idle")}
		third := tick(t, w, c, 150*time.Second)

		if len(h.prompts) != 2 {
			t.Fatalf("prompts = %d, want two per round", len(h.prompts))
		}
		for _, p := range h.prompts {
			if !strings.Contains(p, "widen the loop") || !strings.Contains(p, "go test ./...") {
				t.Fatalf("prompt %q does not carry the goal and the declared checks", p)
			}
		}
		if len(third.Refused) != 1 || !strings.Contains(third.Refused[0], "already nudged 2 times") {
			t.Fatalf("refused = %v, want the round bound named", third.Refused)
		}
	})

	t.Run("never a working agent", func(t *testing.T) {
		b := &fakeBoard{tasks: []board.Task{claimedTask("t-1", "cap", "p1", 1, true)}}
		s, _ := newStorePath(t)
		c := newClock("2026-11-15T10:00:00Z")
		h := &fakeHarness{panes: []agents.Pane{pane("p1", "claude", "working")}}
		w := tierWatcher(b, s, h, &fakeReaper{}, c)

		if _, err := w.Tick(context.Background()); err != nil {
			t.Fatalf("first tick: %v", err)
		}
		for i := 0; i < 10; i++ {
			b.tasks = []board.Task{claimedTask("t-1", "cap", "p1", 1, i%2 == 0)}
			tick(t, w, c, 100*time.Second)
		}
		if len(h.prompts) != 0 {
			t.Fatalf("working agent prompted %d times", len(h.prompts))
		}
	})

	t.Run("never a done task's pane", func(t *testing.T) {
		done := claimedTask("t-1", "cap", "p1", 1, false)
		done.State = "done"
		b := &fakeBoard{tasks: []board.Task{done}}
		s, _ := newStorePath(t)
		c := newClock("2026-11-15T10:00:00Z")
		h := &fakeHarness{panes: []agents.Pane{pane("p1", "claude", "working")}}
		w := tierWatcher(b, s, h, &fakeReaper{}, c)

		if _, err := w.Tick(context.Background()); err != nil {
			t.Fatalf("first tick: %v", err)
		}
		h.panes = []agents.Pane{pane("p1", "claude", "idle")} // a transition, under a done task
		tick(t, w, c, 100*time.Second)
		if len(h.prompts) != 0 {
			t.Fatalf("done task's agent prompted %d times", len(h.prompts))
		}
	})
}

// AC17: an orphaned pane — one that held an agent and lost it — closes only
// after the grace. A pane that never held an agent is a human terminal: never
// closed. A failed close is recorded once and not retried inside the gate.
func TestAC17AnOrphanClosesOnlyWhenEmpty(t *testing.T) {
	b := &fakeBoard{}
	s, _ := newStorePath(t)
	c := newClock("2026-11-15T10:00:00Z")
	h := &fakeHarness{panes: []agents.Pane{
		pane("p-orphan", "claude", "idle"),
		pane("p-agent", "claude", "idle"),
		pane("p-terminal", "", ""),
	}}
	w := tierWatcher(b, s, h, &fakeReaper{}, c)

	if _, err := w.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	h.panes = []agents.Pane{
		pane("p-orphan", "", ""), // its agent ended; its launch row went with the round
		pane("p-agent", "claude", "idle"),
		pane("p-terminal", "", ""),
	}
	tick(t, w, c, 30*time.Second) // the agent ends; the grace starts now
	tick(t, w, c, 20*time.Second) // 20 s of grace: under the minute
	if len(h.closed) != 0 {
		t.Fatalf("closed %v before the grace elapsed", h.closed)
	}
	tick(t, w, c, 40*time.Second) // 60 s of grace: the grace has elapsed
	if len(h.closed) != 1 || h.closed[0] != "p-orphan" {
		t.Fatalf("closed = %v, want exactly [p-orphan]", h.closed)
	}

	for i := 0; i < 5; i++ {
		tick(t, w, c, 100*time.Second)
	}
	if len(h.closed) != 1 {
		t.Fatalf("closed = %v; a pane that never held an agent is a human terminal", h.closed)
	}
}

func TestCloseIsRecordedBeforeItRuns(t *testing.T) {
	b := &fakeBoard{}
	s, _ := newStorePath(t)
	c := newClock("2026-11-15T10:00:00Z")
	h := &fakeHarness{panes: []agents.Pane{pane("p-orphan", "claude", "idle")}, closeErr: errors.New("cannot close")}
	w := tierWatcher(b, s, h, &fakeReaper{}, c)

	if _, err := w.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	h.panes = []agents.Pane{pane("p-orphan", "", "")}
	tick(t, w, c, 30*time.Second)           // the grace starts
	report := tick(t, w, c, 70*time.Second) // the grace has elapsed: close, and it fails
	if len(report.Refused) != 1 || !strings.Contains(report.Refused[0], "failed") {
		t.Fatalf("refused = %v, want the failed close reported", report.Refused)
	}

	as := actionsIn(t, s, state.CursorScope)
	var attempted, failed bool
	for _, a := range as {
		switch {
		case a.Outcome == state.ActionAttempted:
			attempted = true
		case strings.HasPrefix(a.Outcome, state.ActionFailed):
			failed = true
		}
	}
	if !attempted || !failed {
		t.Fatalf("actions = %+v, want an attempted row and a failed row", as)
	}

	tick(t, w, c, 30*time.Second) // inside the gate: no retry
	if len(h.closed) != 1 {
		t.Fatalf("closed %d times, want one attempt inside the gate", len(h.closed))
	}
}

// AC18: a transition into blocked writes one question and one notification,
// sends no prompt and no keys, and writes nothing further while the state holds.
func TestAC18BlockedIsRecordedAndAnnounced(t *testing.T) {
	t.Run("a task's agent", func(t *testing.T) {
		b := &fakeBoard{tasks: []board.Task{claimedTask("t-1", "cap", "p1", 1, true)}}
		s, _ := newStorePath(t)
		c := newClock("2026-11-15T10:00:00Z")
		h := &fakeHarness{panes: []agents.Pane{pane("p1", "claude", "idle")}}
		w := tierWatcher(b, s, h, &fakeReaper{}, c)

		if _, err := w.Tick(context.Background()); err != nil {
			t.Fatalf("first tick: %v", err)
		}
		h.panes = []agents.Pane{pane("p1", "claude", "blocked")}
		report := tick(t, w, c, 30*time.Second)

		qs := questionsIn(t, s, state.Scope("cap"))
		if len(qs) != 1 || qs[0].Task != "t-1" {
			t.Fatalf("questions = %+v, want one question for t-1", qs)
		}
		if len(h.notified) != 1 {
			t.Fatalf("notifications = %d, want one", len(h.notified))
		}
		if len(h.prompts) != 0 || len(h.closed) != 0 {
			t.Fatalf("cap prompted %v or closed %v; a blocked agent is never answered", h.prompts, h.closed)
		}
		if len(report.Actions) != 0 {
			t.Fatalf("actions = %+v; a question is not a tier-1 action", report.Actions)
		}

		held := tick(t, w, c, 30*time.Second)
		if n := len(questionsIn(t, s, state.Scope("cap"))); n != 1 {
			t.Fatalf("questions = %d while blocked did not change, want one", n)
		}
		if len(h.notified) != 1 {
			t.Fatalf("notifications = %d while blocked did not change, want one", len(h.notified))
		}
		if len(held.Actions) != 0 || len(held.Refused) != 0 {
			t.Fatalf("held tick acted: %+v %v", held.Actions, held.Refused)
		}
	})

	t.Run("an unowned pane", func(t *testing.T) {
		b := &fakeBoard{}
		s, _ := newStorePath(t)
		c := newClock("2026-11-15T10:00:00Z")
		h := &fakeHarness{panes: []agents.Pane{pane("p2", "claude", "idle"), pane("p3", "pi", "idle")}}
		w := tierWatcher(b, s, h, &fakeReaper{}, c)

		if _, err := w.Tick(context.Background()); err != nil {
			t.Fatalf("first tick: %v", err)
		}
		// Both unowned panes block in the same tick: one board-wide gap, two
		// announcements.
		h.panes = []agents.Pane{pane("p2", "claude", "blocked"), pane("p3", "pi", "blocked")}
		tick(t, w, c, 30*time.Second)

		if qs := questionsIn(t, s, state.CursorScope); len(qs) != 1 {
			t.Fatalf("questions = %+v, want one board-wide question", qs)
		}
		if len(h.notified) != 2 {
			t.Fatalf("notifications = %d, want one per blocked pane", len(h.notified))
		}
		if len(h.closed) != 0 || len(h.prompts) != 0 {
			t.Fatalf("cap closed %v or prompted %v; an unowned blocked pane is only announced", h.closed, h.prompts)
		}
	})
}

// N5: an unreadable harness skips tier 1 for that tick and never stops the
// board read or the report.
func TestN5UnreadableHarnessSkipsTierOne(t *testing.T) {
	b := &fakeBoard{tasks: []board.Task{claimedTask("t-1", "cap", "p1", 1, true)}}
	s, _ := newStorePath(t)
	c := newClock("2026-11-15T10:00:00Z")
	h := &fakeHarness{panes: []agents.Pane{pane("p1", "claude", "working")}}
	tr := &captureTrace{}
	rp := &fakeReaper{}
	w := tierWatcher(b, s, h, rp, c)
	w.Trace = tr

	seedCursor(t, s, c.at, b.tasks, h.panes)
	h.listErr = errors.New("herdr unavailable")
	b.tasks = nil
	report := tick(t, w, c, 30*time.Second)

	if !report.Changed {
		t.Fatal("unreadable harness dropped the board change from the report")
	}
	if len(rp.calls) != 0 || len(report.Actions) != 0 {
		t.Fatalf("tier 1 ran with an unreadable harness: reap %v, actions %+v", rp.calls, report.Actions)
	}
	found := false
	for _, e := range tr.events {
		if e["harness"] == "error" {
			found = true
		}
	}
	if !found {
		t.Fatal("unreadable harness left no trace event")
	}

	// The transition the failed tick could not act on is not consumed: the
	// next readable tick replays it and reaps.
	h.listErr = nil
	tick(t, w, c, 30*time.Second)
	if len(rp.calls) != 1 || rp.calls[0] != "cap" {
		t.Fatalf("reap calls = %v, want exactly [cap] once the harness recovered", rp.calls)
	}
}

// AC14, baseline: a store with no cursor reaps once for each project whose fs
// pane is still open. A baseline is a first observation, not a timer: the next
// tick, with nothing changed, reaps nothing.
func TestAC14BaselineReapWithoutACursor(t *testing.T) {
	b := &fakeBoard{tasks: []board.Task{
		claimedTask("t-1", "cap", "p1", 1, true),
		claimedTask("t-2", "balcony", "p2", 1, true),
	}}
	s, _ := newStorePath(t)
	c := newClock("2026-11-15T10:00:00Z")
	h := &fakeHarness{panes: []agents.Pane{pane("p1", "claude", "working"), pane("p2", "claude", "working")}}
	rp := &fakeReaper{}
	w := tierWatcher(b, s, h, rp, c)

	first, err := w.Tick(context.Background())
	if err != nil {
		t.Fatalf("baseline tick: %v", err)
	}
	if len(rp.calls) != 2 {
		t.Fatalf("baseline reap calls = %v, want one per project with an open pane", rp.calls)
	}
	reaps := 0
	for _, a := range first.Actions {
		if a.Class == ClassReap && a.Outcome == state.ActionDone {
			reaps++
		}
	}
	if reaps != 2 {
		t.Fatalf("actions = %+v, want two done reaps", first.Actions)
	}
	if len(h.prompts) != 0 || len(h.closed) != 0 {
		t.Fatalf("baseline prompted %v or closed %v; only the reap runs", h.prompts, h.closed)
	}

	second := tick(t, w, c, 30*time.Second)
	if len(rp.calls) != 2 {
		t.Fatalf("reap calls = %v; a baseline is not a timer", rp.calls)
	}
	if len(second.Actions) != 0 || len(second.Refused) != 0 {
		t.Fatalf("unchanged tick acted: actions %+v, refused %v", second.Actions, second.Refused)
	}
}

func actionLines(tr *captureTrace) []map[string]any {
	var out []map[string]any
	for _, e := range tr.events {
		if e["kind"] == "action" {
			out = append(out, e)
		}
	}
	return out
}

// wantActionLine returns the trace's single action line of a class and checks
// the fields AC20 names.
func wantActionLine(t *testing.T, tr *captureTrace, class string) map[string]any {
	t.Helper()
	var got []map[string]any
	for _, e := range actionLines(tr) {
		if e["class"] == class {
			got = append(got, e)
		}
	}
	if len(got) != 1 {
		t.Fatalf("trace action lines of class %s = %d, want one: %v", class, len(got), actionLines(tr))
	}
	e := got[0]
	for _, k := range []string{"at", "target", "project", "task", "pane", "trigger", "result"} {
		if _, ok := e[k]; !ok {
			t.Fatalf("action line %v has no %q", e, k)
		}
	}
	return e
}

// AC20: every tier-1 decision writes one action line to the trace — class,
// target, project, task, pane, trigger and result. A gated repeat writes a
// refused line; a tick with no decision writes none.
func TestAC20TierOneDecisionsAreTraced(t *testing.T) {
	t.Run("reap", func(t *testing.T) {
		b := &fakeBoard{tasks: []board.Task{claimedTask("t-1", "cap", "p1", 1, true)}}
		s, _ := newStorePath(t)
		c := newClock("2026-11-15T10:00:00Z")
		h := &fakeHarness{panes: []agents.Pane{pane("p1", "claude", "working")}}
		w := tierWatcher(b, s, h, &fakeReaper{}, c)
		tr := &captureTrace{}
		w.Trace = tr

		seedCursor(t, s, c.at, b.tasks, h.panes)
		b.tasks = nil
		tick(t, w, c, 30*time.Second)

		e := wantActionLine(t, tr, ClassReap)
		if e["result"] != state.ActionDone || e["project"] != "cap" || e["trigger"] == "" {
			t.Fatalf("reap line = %v, want done with the project and trigger", e)
		}
	})

	t.Run("close", func(t *testing.T) {
		b := &fakeBoard{}
		s, _ := newStorePath(t)
		c := newClock("2026-11-15T10:00:00Z")
		h := &fakeHarness{panes: []agents.Pane{pane("p-orphan", "claude", "idle")}}
		w := tierWatcher(b, s, h, &fakeReaper{}, c)
		tr := &captureTrace{}
		w.Trace = tr

		if _, err := w.Tick(context.Background()); err != nil {
			t.Fatalf("first tick: %v", err)
		}
		h.panes = []agents.Pane{pane("p-orphan", "", "")}
		tick(t, w, c, 30*time.Second) // the agent ends; the grace starts
		tick(t, w, c, 61*time.Second) // the grace elapsed: close

		e := wantActionLine(t, tr, ClassClose)
		if e["result"] != state.ActionDone || e["target"] != "p-orphan" || e["pane"] != "p-orphan" {
			t.Fatalf("close line = %v, want done on the pane", e)
		}
	})

	t.Run("nudge", func(t *testing.T) {
		b := &fakeBoard{tasks: []board.Task{claimedTask("t-1", "cap", "p1", 1, true)}}
		s, _ := newStorePath(t)
		c := newClock("2026-11-15T10:00:00Z")
		h := &fakeHarness{panes: []agents.Pane{pane("p1", "claude", "idle")}}
		w := tierWatcher(b, s, h, &fakeReaper{}, c)
		tr := &captureTrace{}
		w.Trace = tr

		seedCursor(t, s, c.at, b.tasks, h.panes)
		b.tasks = []board.Task{claimedTask("t-1", "cap", "p1", 1, false)}
		tick(t, w, c, 30*time.Second)

		e := wantActionLine(t, tr, ClassNudge)
		if e["result"] != state.ActionDone || e["task"] != "t-1" || e["pane"] != "p1" {
			t.Fatalf("nudge line = %v, want done on the task and pane", e)
		}
	})

	t.Run("blocked", func(t *testing.T) {
		b := &fakeBoard{tasks: []board.Task{claimedTask("t-1", "cap", "p1", 1, true)}}
		s, _ := newStorePath(t)
		c := newClock("2026-11-15T10:00:00Z")
		h := &fakeHarness{panes: []agents.Pane{pane("p1", "claude", "idle")}}
		w := tierWatcher(b, s, h, &fakeReaper{}, c)
		tr := &captureTrace{}
		w.Trace = tr

		seedCursor(t, s, c.at, b.tasks, h.panes)
		h.panes = []agents.Pane{pane("p1", "claude", "blocked")}
		tick(t, w, c, 30*time.Second)

		e := wantActionLine(t, tr, ClassBlocked)
		if e["result"] != state.ActionDone || e["task"] != "t-1" || e["pane"] != "p1" {
			t.Fatalf("blocked line = %v, want done on the task and pane", e)
		}
	})

	t.Run("gate refusal", func(t *testing.T) {
		b := &fakeBoard{tasks: []board.Task{claimedTask("t-1", "cap", "p1", 1, true)}}
		s, _ := newStorePath(t)
		c := newClock("2026-11-15T10:00:00Z")
		h := &fakeHarness{panes: []agents.Pane{pane("p1", "claude", "idle")}}
		w := tierWatcher(b, s, h, &fakeReaper{}, c)
		tr := &captureTrace{}
		w.Trace = tr

		seedCursor(t, s, c.at, b.tasks, h.panes)
		b.tasks = []board.Task{claimedTask("t-1", "cap", "p1", 1, false)}
		tick(t, w, c, 30*time.Second) // nudge 1
		h.panes = []agents.Pane{pane("p1", "claude", "working")}
		tick(t, w, c, 30*time.Second)
		h.panes = []agents.Pane{pane("p1", "claude", "idle")}
		tick(t, w, c, 30*time.Second) // inside the gate: refused

		var refused []map[string]any
		for _, e := range actionLines(tr) {
			if e["result"] == actionRefused {
				refused = append(refused, e)
			}
		}
		if len(refused) != 1 || refused[0]["class"] != ClassNudge {
			t.Fatalf("refused lines = %v, want one nudge refusal", refused)
		}
	})

	t.Run("unchanged tick", func(t *testing.T) {
		b := &fakeBoard{}
		s, _ := newStorePath(t)
		c := newClock("2026-11-15T10:00:00Z")
		h := &fakeHarness{panes: []agents.Pane{pane("p2", "claude", "working")}}
		w := tierWatcher(b, s, h, &fakeReaper{}, c)
		tr := &captureTrace{}
		w.Trace = tr

		if _, err := w.Tick(context.Background()); err != nil {
			t.Fatalf("first tick: %v", err)
		}
		tick(t, w, c, 30*time.Second)

		if n := len(actionLines(tr)); n != 0 {
			t.Fatalf("action lines = %d on a tick with no decision: %v", n, actionLines(tr))
		}
	})
}

// AC14, the harness transition: fs keeps a working agent at its round's close.
// When the pane stops working, the tick asks fs again; cap still never closes
// the pane. A round still open is not reaped on that transition.
func TestAC14ReapsWhenTheAgentStopsWorking(t *testing.T) {
	t.Run("a closed round", func(t *testing.T) {
		done := claimedTask("t-1", "cap", "p1", 1, false)
		done.State = "done"
		b := &fakeBoard{tasks: []board.Task{done}}
		s, _ := newStorePath(t)
		c := newClock("2026-11-15T10:00:00Z")
		h := &fakeHarness{panes: []agents.Pane{pane("p1", "claude", "working")}}
		rp := &fakeReaper{}
		w := tierWatcher(b, s, h, rp, c)
		seedCursor(t, s, c.at, b.tasks, h.panes)

		h.panes = []agents.Pane{pane("p1", "claude", "idle")}
		report := tick(t, w, c, 30*time.Second)

		if len(rp.calls) != 1 || rp.calls[0] != "cap" {
			t.Fatalf("reap calls = %v, want exactly [cap] when the pane stops working", rp.calls)
		}
		if len(h.closed) != 0 {
			t.Fatalf("cap closed %v; an fs pane is reaped by fs, never closed by cap", h.closed)
		}
		reaped := false
		for _, a := range report.Actions {
			if a.Class == ClassReap && a.Outcome == state.ActionDone {
				reaped = true
			}
		}
		if !reaped {
			t.Fatalf("actions = %+v, want one done reap", report.Actions)
		}

		quiet := tick(t, w, c, 30*time.Second)
		if len(rp.calls) != 1 {
			t.Fatalf("reap calls = %v; the transition reaped once", rp.calls)
		}
		if len(quiet.Actions) != 0 {
			t.Fatalf("actions = %+v on an unchanged tick", quiet.Actions)
		}
	})

	t.Run("a failed review", func(t *testing.T) {
		at, _ := time.Parse(time.RFC3339, "2026-11-15T10:00:00Z")
		failed := claimedTask("t-2", "cap", "p2", 1, true)
		failed.Kind = "review"
		failed.Agent = board.Observed{Role: "review", State: "fail", Reported: "fail", At: &at}
		b := &fakeBoard{tasks: []board.Task{failed}}
		s, _ := newStorePath(t)
		c := newClock("2026-11-15T10:00:00Z")
		h := &fakeHarness{panes: []agents.Pane{pane("p2", "claude", "working")}}
		rp := &fakeReaper{}
		w := tierWatcher(b, s, h, rp, c)
		seedCursor(t, s, c.at, b.tasks, h.panes)

		h.panes = []agents.Pane{pane("p2", "claude", "idle")}
		tick(t, w, c, 30*time.Second)

		if len(rp.calls) != 1 || rp.calls[0] != "cap" {
			t.Fatalf("reap calls = %v, want exactly [cap] after a failed review's agent stops", rp.calls)
		}
	})

	t.Run("the round is still open", func(t *testing.T) {
		b := &fakeBoard{tasks: []board.Task{claimedTask("t-1", "cap", "p1", 1, true)}}
		s, _ := newStorePath(t)
		c := newClock("2026-11-15T10:00:00Z")
		h := &fakeHarness{panes: []agents.Pane{pane("p1", "claude", "working")}}
		rp := &fakeReaper{}
		w := tierWatcher(b, s, h, rp, c)
		seedCursor(t, s, c.at, b.tasks, h.panes)

		h.panes = []agents.Pane{pane("p1", "claude", "idle")}
		tick(t, w, c, 30*time.Second)

		if len(rp.calls) != 0 {
			t.Fatalf("reap calls = %v; the task's round is still open", rp.calls)
		}
	})
}

// AC21: a dev task leaving the board is announced as a closed round; the
// review task that closed the same round is not announced twice.
func TestAC21AClosedRoundIsAnnounced(t *testing.T) {
	t.Run("a dev task", func(t *testing.T) {
		b := &fakeBoard{tasks: []board.Task{claimedTask("t-1", "cap", "p1", 1, true)}}
		s, _ := newStorePath(t)
		c := newClock("2026-11-15T10:00:00Z")
		h := &fakeHarness{panes: []agents.Pane{pane("p1", "claude", "working")}}
		w := tierWatcher(b, s, h, &fakeReaper{}, c)
		seedCursor(t, s, c.at, b.tasks, h.panes)

		b.tasks = nil
		tick(t, w, c, 30*time.Second)
		if len(h.notified) != 1 {
			t.Fatalf("notifications = %v, want one round-close line", h.notified)
		}
		if !strings.Contains(h.notified[0], "cap: round closed") || !strings.Contains(h.notified[0], "cap t-1 done, review passed") {
			t.Fatalf("notification = %q, want the round-close line naming the project and task", h.notified[0])
		}

		tick(t, w, c, 30*time.Second)
		if len(h.notified) != 1 {
			t.Fatalf("notifications = %v; a closed round is announced once", h.notified)
		}
	})

	t.Run("a review task", func(t *testing.T) {
		rev := claimedTask("t-2", "cap", "p2", 1, true)
		rev.Kind = "review"
		b := &fakeBoard{tasks: []board.Task{rev}}
		s, _ := newStorePath(t)
		c := newClock("2026-11-15T10:00:00Z")
		h := &fakeHarness{panes: []agents.Pane{pane("p2", "claude", "working")}}
		w := tierWatcher(b, s, h, &fakeReaper{}, c)
		seedCursor(t, s, c.at, b.tasks, h.panes)

		b.tasks = nil
		tick(t, w, c, 30*time.Second)

		if len(h.notified) != 0 {
			t.Fatalf("notifications = %v; a review closes the round its dev task announces", h.notified)
		}
	})

	t.Run("no closed round", func(t *testing.T) {
		b := &fakeBoard{tasks: []board.Task{claimedTask("t-1", "cap", "p1", 1, true)}}
		s, _ := newStorePath(t)
		c := newClock("2026-11-15T10:00:00Z")
		h := &fakeHarness{panes: []agents.Pane{pane("p1", "claude", "working")}}
		w := tierWatcher(b, s, h, &fakeReaper{}, c)
		seedCursor(t, s, c.at, b.tasks, h.panes)

		tick(t, w, c, 30*time.Second)
		tick(t, w, c, 30*time.Second)

		if len(h.notified) != 0 {
			t.Fatalf("notifications = %v on an unchanged board", h.notified)
		}
	})
}

// seedClaim writes the cap.claim row a claiming session leaves, so the tick has
// an address to report to.
func seedClaim(t *testing.T, s state.Store, session, paneID string, expires time.Time) {
	t.Helper()
	text, err := state.Encode(state.Claim{Session: session, Pane: paneID, At: expires.Add(-state.ClaimLease), Expires: expires})
	if err != nil {
		t.Fatalf("encode claim: %v", err)
	}
	if _, err := s.Put(state.KindClaim, state.CursorScope, "claim "+session, text); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
}

func TestAC22TheTickInformsTheClaimHolder(t *testing.T) {
	// finished builds a watcher whose one dev task just left the board: one
	// event, and the reap that ends its round.
	finished := func(t *testing.T) (*fakeHarness, state.Store, *Watcher, *clock) {
		t.Helper()
		task := claimedTask("t-1", "cap", "p1", 1, true)
		task.Goal = "fix the AC14 hole"
		b := &fakeBoard{tasks: []board.Task{task}}
		s, _ := newStorePath(t)
		c := newClock("2026-11-15T10:00:00Z")
		h := &fakeHarness{panes: []agents.Pane{pane("p1", "claude", "working"), pane("p9", "claude", "idle")}}
		w := tierWatcher(b, s, h, &fakeReaper{}, c)
		seedCursor(t, s, c.at, b.tasks, h.panes)
		b.tasks = nil
		return h, s, w, c
	}

	t.Run("one prompt naming the finished round by goal", func(t *testing.T) {
		h, s, w, c := finished(t)
		seedClaim(t, s, "s-coord", "p9", c.at.Add(state.ClaimLease))

		r := tick(t, w, c, 30*time.Second)

		if len(h.prompts) != 1 {
			t.Fatalf("prompts = %v, want one to the claim holder", h.prompts)
		}
		if !strings.HasPrefix(h.prompts[0], "p9: ") {
			t.Fatalf("prompt went to %q, want pane p9", h.prompts[0])
		}
		for _, want := range []string{"finished", "cap", `"fix the AC14 hole"`, "review passed"} {
			if !strings.Contains(h.prompts[0], want) {
				t.Fatalf("prompt = %q, want it to name %q", h.prompts[0], want)
			}
		}
		if strings.Contains(h.prompts[0], "t-1") {
			t.Fatalf("prompt = %q; tasks are named by goal, never by id", h.prompts[0])
		}
		if len(h.notified) != 0 {
			t.Fatalf("notifications = %v; the holder was informed, so the toast stands down", h.notified)
		}

		var informs []state.Action
		for _, a := range actionsIn(t, s, state.CursorScope) {
			if a.Class == ClassInform && a.Outcome != state.ActionAttempted {
				informs = append(informs, a)
			}
		}
		if len(informs) != 1 || informs[0].Outcome != state.ActionDone || informs[0].Pane != "p9" {
			t.Fatalf("inform rows = %+v, want one done row naming pane p9", informs)
		}
		if len(r.Actions) < 2 {
			t.Fatalf("report actions = %+v, want the reap and the inform", r.Actions)
		}
	})

	t.Run("a blocked agent is named with its pane", func(t *testing.T) {
		task := claimedTask("t-1", "cap", "p1", 1, true)
		task.Goal = "fix the AC14 hole"
		b := &fakeBoard{tasks: []board.Task{task}}
		s, _ := newStorePath(t)
		c := newClock("2026-11-15T10:00:00Z")
		h := &fakeHarness{panes: []agents.Pane{pane("p1", "claude", "idle"), pane("p9", "claude", "idle")}}
		w := tierWatcher(b, s, h, &fakeReaper{}, c)
		seedCursor(t, s, c.at, b.tasks, h.panes)
		seedClaim(t, s, "s-coord", "p9", c.at.Add(state.ClaimLease))

		h.panes = []agents.Pane{pane("p1", "claude", "blocked"), pane("p9", "claude", "idle")}
		tick(t, w, c, 30*time.Second)

		if len(h.prompts) != 1 || !strings.Contains(h.prompts[0], "blocked cap:") || !strings.Contains(h.prompts[0], "p1") {
			t.Fatalf("prompts = %v, want one blocked line naming the blocker's pane", h.prompts)
		}
		if len(h.notified) != 0 {
			t.Fatalf("notifications = %v; the holder was informed, so the toast stands down", h.notified)
		}
	})

	t.Run("no claim", func(t *testing.T) {
		h, _, w, c := finished(t)
		tick(t, w, c, 30*time.Second)
		if len(h.prompts) != 0 {
			t.Fatalf("prompts = %v; no claim means no address", h.prompts)
		}
		if len(h.notified) != 1 {
			t.Fatalf("notifications = %v; with no holder the toast is the fallback", h.notified)
		}
	})

	t.Run("stale claim", func(t *testing.T) {
		h, s, w, c := finished(t)
		seedClaim(t, s, "s-coord", "p9", c.at.Add(-time.Minute))
		tick(t, w, c, 30*time.Second)
		if len(h.prompts) != 0 {
			t.Fatalf("prompts = %v; a stale claim has no address", h.prompts)
		}
	})

	t.Run("claim without a pane", func(t *testing.T) {
		h, s, w, c := finished(t)
		seedClaim(t, s, "s-coord", "", c.at.Add(state.ClaimLease))
		tick(t, w, c, 30*time.Second)
		if len(h.prompts) != 0 {
			t.Fatalf("prompts = %v; a claim with no pane has no address", h.prompts)
		}
	})

	t.Run("the pane is not listed", func(t *testing.T) {
		h, s, w, c := finished(t)
		seedClaim(t, s, "s-coord", "p-gone", c.at.Add(state.ClaimLease))
		tick(t, w, c, 30*time.Second)
		if len(h.prompts) != 0 {
			t.Fatalf("prompts = %v; an unlisted pane is not an address", h.prompts)
		}
	})

	t.Run("the holder is blocked", func(t *testing.T) {
		task := claimedTask("t-1", "cap", "p1", 1, true)
		b := &fakeBoard{tasks: []board.Task{task}}
		s, _ := newStorePath(t)
		c := newClock("2026-11-15T10:00:00Z")
		h := &fakeHarness{panes: []agents.Pane{pane("p1", "claude", "working"), pane("p9", "claude", "blocked")}}
		w := tierWatcher(b, s, h, &fakeReaper{}, c)
		seedCursor(t, s, c.at, b.tasks, h.panes)
		seedClaim(t, s, "s-coord", "p9", c.at.Add(state.ClaimLease))

		b.tasks = nil
		tick(t, w, c, 30*time.Second)

		if len(h.prompts) != 0 {
			t.Fatalf("prompts = %v; a blocked pane is never prompted", h.prompts)
		}
	})

	t.Run("a tick with no event", func(t *testing.T) {
		task := claimedTask("t-1", "cap", "p1", 1, true)
		b := &fakeBoard{tasks: []board.Task{task}}
		s, _ := newStorePath(t)
		c := newClock("2026-11-15T10:00:00Z")
		h := &fakeHarness{panes: []agents.Pane{pane("p1", "claude", "working"), pane("p9", "claude", "idle")}}
		w := tierWatcher(b, s, h, &fakeReaper{}, c)
		seedCursor(t, s, c.at, b.tasks, h.panes)
		seedClaim(t, s, "s-coord", "p9", c.at.Add(state.ClaimLease))

		tick(t, w, c, 30*time.Second)
		if len(h.prompts) != 0 {
			t.Fatalf("prompts = %v; a tick that saw no event has nothing to report", h.prompts)
		}
	})

	t.Run("a failed prompt is traced and not retried", func(t *testing.T) {
		h, s, w, c := finished(t)
		h.promptErr = errors.New("pane gone")
		seedClaim(t, s, "s-coord", "p9", c.at.Add(state.ClaimLease))

		tick(t, w, c, 30*time.Second)

		if len(h.prompts) != 1 {
			t.Fatalf("prompts = %v; a failed prompt is not retried in the tick", h.prompts)
		}
		if len(h.notified) != 1 {
			t.Fatalf("notifications = %v; a failed prompt leaves the event unannounced", h.notified)
		}
		var informs []state.Action
		for _, a := range actionsIn(t, s, state.CursorScope) {
			if a.Class == ClassInform && a.Outcome != state.ActionAttempted {
				informs = append(informs, a)
			}
		}
		if len(informs) != 1 || !strings.HasPrefix(informs[0].Outcome, state.ActionFailed) {
			t.Fatalf("inform rows = %+v, want one failed row", informs)
		}
	})
}
