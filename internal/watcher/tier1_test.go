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

	if _, err := w.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	if len(rp.calls) != 0 {
		t.Fatalf("first tick reaped %v; nothing had changed yet", rp.calls)
	}

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

// AC14, round ended without the task closing: a lapsed lease or a fresh review
// verdict still reaps the project while the pane is open.
func TestAC14ReapsARoundThatEndedOnTheBoard(t *testing.T) {
	t.Run("lease lapsed", func(t *testing.T) {
		b := &fakeBoard{tasks: []board.Task{claimedTask("t-1", "cap", "p1", 1, true)}}
		s, _ := newStorePath(t)
		c := newClock("2026-11-15T10:00:00Z")
		h := &fakeHarness{panes: []agents.Pane{pane("p1", "claude", "idle")}}
		rp := &fakeReaper{}
		w := tierWatcher(b, s, h, rp, c)

		if _, err := w.Tick(context.Background()); err != nil {
			t.Fatalf("first tick: %v", err)
		}
		b.tasks = []board.Task{claimedTask("t-1", "cap", "p1", 1, false)}
		report := tick(t, w, c, 30*time.Second)

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

		if _, err := w.Tick(context.Background()); err != nil {
			t.Fatalf("first tick: %v", err)
		}
		if len(rp.calls) != 0 {
			t.Fatalf("reap calls = %v; the verdict was already there at baseline", rp.calls)
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

	if _, err := w.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}

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

	if _, err := w.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
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
