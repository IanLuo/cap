package watcher

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IanLuo/cap/internal/board"
	"github.com/IanLuo/cap/internal/state"
)

func ctx() context.Context { return context.Background() }

func openCursorID(t *testing.T, s *state.MemStore) int64 {
	t.Helper()
	for _, r := range openRows(t, s, "cap") {
		if r.Kind == state.KindCursor {
			return r.ID
		}
	}
	rows, err := s.Recent(state.CursorScope, 100)
	if err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	for _, r := range rows {
		if r.Kind == state.KindCursor && !r.Closed {
			return r.ID
		}
	}
	return 0
}

// AC2: one open cursor per changed board, replaced only on change; one open act
// per instruction.
func TestAC2OneRecordPerEvent(t *testing.T) {
	b := &fakeBoard{tasks: []board.Task{{Task: "t-1", Project: "p", Kind: "dev", State: "open", ProjectPath: "/tmp/p"}}}
	s, path := newStorePath(t)
	c := newClock("2026-11-07T12:00:00Z")
	w := testWatcher(b, s, c)

	if _, err := w.Tick(ctx()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	if n := countOpen(t, s, state.CursorScope, state.KindCursor); n != 1 {
		t.Fatalf("open cursors = %d, want 1", n)
	}
	first := openCursorID(t, s)

	if _, err := w.Tick(ctx()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if n := countOpen(t, s, state.CursorScope, state.KindCursor); n != 1 {
		t.Fatalf("open cursors after unchanged tick = %d, want 1", n)
	}
	if got := openCursorID(t, s); got != first {
		t.Fatalf("unchanged tick replaced cursor %d with %d", first, got)
	}

	seedKnowledge(t, path, "cap.dispatch: echo {task}\ncap.agent: fake")
	d := testDispatcher(b, s, &fakeLauncher{}, c)
	if _, err := d.Act(ctx(), "t-1"); err != nil {
		t.Fatalf("act: %v", err)
	}
	if n := countOpen(t, s, state.Scope("p"), state.KindAct); n != 1 {
		t.Fatalf("open acts = %d, want 1", n)
	}
}

// AC3: a stranger combines the fs row with the cap.act record and knows what
// happened. The record carries command, agent, knowledge id, time and observed
// result; neither source repeats the other.
func TestAC3StrangerReconstructsTheAct(t *testing.T) {
	b := &fakeBoard{tasks: []board.Task{{Task: "t-1", Project: "p", Kind: "dev", State: "open", ProjectPath: "/tmp/p"}}}
	s, path := newStorePath(t)
	c := newClock("2026-11-07T12:00:00Z")
	kid := seedKnowledge(t, path, "cap.dispatch: echo {task}\ncap.agent: fake")
	l := &fakeLauncher{}
	d := testDispatcher(b, s, l, c)

	if _, err := d.Act(ctx(), "t-1"); err != nil {
		t.Fatalf("act: %v", err)
	}
	if len(l.calls) != 1 || l.calls[0].Dir != "/tmp/p" || l.calls[0].Command != "echo t-1" {
		t.Fatalf("launch calls = %+v, want one echo t-1 in /tmp/p", l.calls)
	}
	a := openAct(t, s, "p", "t-1")
	if a.Command != "echo t-1" || a.Agent != "fake" || a.Knowledge == "" || a.LaunchedAt.IsZero() || a.Observed != state.ObservedUnconfirmed {
		t.Fatalf("act record is not reconstructable: %+v", a)
	}
	if a.Knowledge != strconv.FormatInt(kid, 10) {
		t.Fatalf("knowledge id = %s, want %d", a.Knowledge, kid)
	}

	b.tasks[0].State = "claimed"
	b.tasks[0].Session = "sess-1"
	b.tasks[0].LeaseLive = true
	w := testWatcher(b, s, c)
	if _, err := w.Tick(ctx()); err != nil {
		t.Fatalf("confirming tick: %v", err)
	}
	a = openAct(t, s, "p", "t-1")
	if a.Observed != state.ObservedConfirmed || a.Session != "sess-1" || a.ConfirmedAt == nil {
		t.Fatalf("act was not confirmed from the holder: %+v", a)
	}
}

// AC4: confirmed means held. A task whose launcher claims is confirmed on a
// later tick; a task whose launcher never claims stays unconfirmed and is named
// in the report. cap never marks a task failed.
func TestAC4ConfirmedMeansHeld(t *testing.T) {
	b := &fakeBoard{tasks: []board.Task{
		{Task: "t-claim", Project: "p", Kind: "dev", State: "open", ProjectPath: "/tmp/p"},
		{Task: "t-never", Project: "p", Kind: "dev", State: "open", ProjectPath: "/tmp/p"},
	}}
	s, path := newStorePath(t)
	c := newClock("2026-11-07T12:00:00Z")
	seedKnowledge(t, path, "cap.dispatch: run {task}\ncap.agent: fake")
	d := testDispatcher(b, s, &fakeLauncher{}, c)
	for _, tid := range []string{"t-claim", "t-never"} {
		if _, err := d.Act(ctx(), tid); err != nil {
			t.Fatalf("act %s: %v", tid, err)
		}
	}

	b.tasks[0].State = "claimed"
	b.tasks[0].Session = "sess-claim"
	b.tasks[0].LeaseLive = true
	w := testWatcher(b, s, c)
	report, err := w.Tick(ctx())
	if err != nil {
		t.Fatalf("later tick: %v", err)
	}
	if a := openAct(t, s, "p", "t-claim"); a.Observed != state.ObservedConfirmed {
		t.Fatalf("t-claim observed = %s, want confirmed", a.Observed)
	}
	if a := openAct(t, s, "p", "t-never"); a.Observed != state.ObservedUnconfirmed {
		t.Fatalf("t-never observed = %s, want unconfirmed", a.Observed)
	}
	if len(report.Pending) != 1 || report.Pending[0].Task != "t-never" {
		t.Fatalf("report pending = %+v, want t-never named", report.Pending)
	}

	before := countOpen(t, s, state.Scope("p"), state.KindAct)
	if _, err := w.Tick(ctx()); err != nil {
		t.Fatalf("unchanged tick: %v", err)
	}
	if n := countOpen(t, s, state.Scope("p"), state.KindAct); n != before {
		t.Fatalf("no-holder tick wrote another act: %d -> %d", before, n)
	}
}

// AC4a: a recipe is scoped to the projects it names. One record scoped to
// project a, one unscoped. A task in b sees only the unscoped record; a task in
// c, named by no record, asks and launches nothing.
func TestAC4aRecipeScopedToProjects(t *testing.T) {
	b := &fakeBoard{tasks: []board.Task{
		{Task: "t-b", Project: "b", Kind: "dev", State: "open", ProjectPath: "/tmp/b"},
		{Task: "t-c", Project: "c", Kind: "dev", State: "open", ProjectPath: "/tmp/c"},
	}}
	s, path := newStorePath(t)
	c := newClock("2026-11-07T12:00:00Z")
	seedKnowledge(t, path, "cap.dispatch: scoped {task}\ncap.agent: fake\ncap.project: a, b")
	seedKnowledge(t, path, "cap.dispatch: every {task}\ncap.agent: fake")

	// Project b is named by both records: the set is ambiguous, so cap asks.
	l := &fakeLauncher{}
	d := testDispatcher(b, s, l, c)
	if _, err := d.Act(ctx(), "t-b"); err == nil || !IsRefusal(err) {
		t.Fatalf("act with two matching recipes = %v, want refusal", err)
	}
	if len(l.calls) != 0 {
		t.Fatalf("launched %d commands on an ambiguous set", len(l.calls))
	}

	// A task in c matches only the unscoped record, so it launches that one.
	if _, err := d.Act(ctx(), "t-c"); err != nil {
		t.Fatalf("act in an unscoped project: %v", err)
	}
	if len(l.calls) != 1 {
		t.Fatalf("launches = %d, want 1", len(l.calls))
	}
	if got := l.calls[0].Command; !strings.Contains(got, "every t-c") || strings.Contains(got, "scoped") {
		t.Fatalf("launched %q, want the unscoped record only", got)
	}
}

// A scoped record alone must not launch in a project it does not name.
func TestAC4aScopedRecordDoesNotMatchAnotherProject(t *testing.T) {
	b := &fakeBoard{tasks: []board.Task{
		{Task: "t-a", Project: "a", Kind: "dev", State: "open", ProjectPath: "/tmp/a"},
		{Task: "t-c", Project: "c", Kind: "dev", State: "open", ProjectPath: "/tmp/c"},
	}}
	s, path := newStorePath(t)
	c := newClock("2026-11-07T12:00:00Z")
	seedKnowledge(t, path, "cap.dispatch: scoped {task}\ncap.agent: fake\ncap.project: a")
	l := &fakeLauncher{}
	d := testDispatcher(b, s, l, c)

	if _, err := d.Act(ctx(), "t-a"); err != nil {
		t.Fatalf("act in the named project: %v", err)
	}
	if _, err := d.Act(ctx(), "t-c"); err == nil || !IsRefusal(err) {
		t.Fatalf("act in an unnamed project = %v, want refusal", err)
	}
	if len(l.calls) != 1 {
		t.Fatalf("launches = %d, want 1", len(l.calls))
	}
	if n := countOpen(t, s, state.Scope("c"), state.KindQuestion); n != 1 {
		t.Fatalf("questions in c = %d, want 1", n)
	}
	if n := countOpen(t, s, state.Scope("c"), state.KindAct); n != 0 {
		t.Fatalf("acts in c = %d, want 0", n)
	}
}

// AC4b: a review inherits its parent dev task's path. fs records the path on
// the dev task, not on the review.
func TestAC4bReviewInheritsParentPath(t *testing.T) {
	b := &fakeBoard{tasks: []board.Task{
		{Task: "t-dev", Project: "p", Kind: "dev", State: "done", ProjectPath: "/tmp/p"},
		{Task: "t-rev", Project: "p", Kind: "review", State: "open", ParentDev: "t-dev"},
	}}
	s, path := newStorePath(t)
	c := newClock("2026-11-07T12:00:00Z")
	seedKnowledge(t, path, "cap.dispatch: echo {task} in {path}\ncap.agent: fake\ncap.task-kind: review")
	l := &fakeLauncher{}
	d := testDispatcher(b, s, l, c)

	if _, err := d.Act(ctx(), "t-rev"); err != nil {
		t.Fatalf("act on a review with no path: %v", err)
	}
	if len(l.calls) != 1 {
		t.Fatalf("launches = %d, want 1", len(l.calls))
	}
	if l.calls[0].Dir != "/tmp/p" {
		t.Fatalf("launched in %q, want the parent's path", l.calls[0].Dir)
	}
	if n := countOpen(t, s, state.Scope("p"), state.KindQuestion); n != 0 {
		t.Fatalf("questions = %d, want 0", n)
	}
}

// AC4b: with no path anywhere, cap still asks and launches nothing.
func TestAC4bNoPathAnywhereAsks(t *testing.T) {
	b := &fakeBoard{tasks: []board.Task{
		{Task: "t-rev", Project: "p", Kind: "review", State: "open", ParentDev: "t-missing"},
	}}
	s, path := newStorePath(t)
	c := newClock("2026-11-07T12:00:00Z")
	seedKnowledge(t, path, "cap.dispatch: echo {task}\ncap.agent: fake\ncap.task-kind: review")
	l := &fakeLauncher{}
	d := testDispatcher(b, s, l, c)

	if _, err := d.Act(ctx(), "t-rev"); err == nil || !IsRefusal(err) {
		t.Fatalf("act with no path = %v, want refusal", err)
	}
	if len(l.calls) != 0 {
		t.Fatalf("launches = %d, want 0", len(l.calls))
	}
	if n := countOpen(t, s, state.Scope("p"), state.KindQuestion); n != 1 {
		t.Fatalf("questions = %d, want 1", n)
	}
}

// AC5: missing knowledge asks once. One question, no command, fs unchanged, and
// a second instruction for the same gap opens no second question.
func TestAC5MissingKnowledgeAsksOnce(t *testing.T) {
	b := &fakeBoard{tasks: []board.Task{{Task: "t-1", Project: "p", Kind: "dev", State: "open", ProjectPath: "/tmp/p"}}}
	s, _ := newStorePath(t)
	c := newClock("2026-11-07T12:00:00Z")
	l := &fakeLauncher{}
	d := testDispatcher(b, s, l, c)

	if _, err := d.Act(ctx(), "t-1"); err == nil || !IsRefusal(err) {
		t.Fatalf("act with no knowledge = %v, want refusal", err)
	}
	if len(l.calls) != 0 {
		t.Fatalf("launched %d commands without knowledge", len(l.calls))
	}
	if n := countOpen(t, s, state.Scope("p"), state.KindQuestion); n != 1 {
		t.Fatalf("questions = %d, want 1", n)
	}
	if n := countOpen(t, s, state.Scope("p"), state.KindAct); n != 0 {
		t.Fatalf("acts = %d, want 0", n)
	}
	if _, err := d.Act(ctx(), "t-1"); err == nil || !IsRefusal(err) {
		t.Fatalf("second act = %v, want refusal", err)
	}
	if n := countOpen(t, s, state.Scope("p"), state.KindQuestion); n != 1 {
		t.Fatalf("second instruction opened another question: %d", n)
	}
	if b.calls != 2 {
		t.Fatalf("board calls = %d, want 2 (status only)", b.calls)
	}
}

// AC6: cap writes nothing in fs. After a tick and an act, every row cap owns is
// a cap kind in a cap scope; the board seam was only ever asked to read.
func TestAC6CapWritesNothingInFS(t *testing.T) {
	b := &fakeBoard{tasks: []board.Task{{Task: "t-1", Project: "p", Kind: "dev", State: "open", ProjectPath: "/tmp/p"}}}
	s, path := newStorePath(t)
	c := newClock("2026-11-07T12:00:00Z")
	seedKnowledge(t, path, "cap.dispatch: echo {task}\ncap.agent: fake")
	w := testWatcher(b, s, c)
	if _, err := w.Tick(ctx()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	d := testDispatcher(b, s, &fakeLauncher{}, c)
	if _, err := d.Act(ctx(), "t-1"); err != nil {
		t.Fatalf("act: %v", err)
	}

	for _, scope := range []string{state.CursorScope, state.Scope("p")} {
		rows, err := s.Recent(scope, 1000)
		if err != nil {
			t.Fatalf("read %s: %v", scope, err)
		}
		for _, r := range rows {
			switch r.Kind {
			case state.KindAct, state.KindQuestion, state.KindCursor:
			default:
				t.Fatalf("cap wrote foreign kind %q in %s", r.Kind, scope)
			}
			if strings.HasPrefix(r.Kind, "fs.") {
				t.Fatalf("cap wrote an fs row: %s", r.Kind)
			}
		}
	}
	if b.calls != 2 {
		t.Fatalf("board calls = %d, want one per tick/act and no write", b.calls)
	}
}

// AC7: an unheld review ranks before an unheld dev; dev-only ranks oldest first;
// an unconfirmed act younger than the gate is not recommended again.
func TestAC7TheRankHolds(t *testing.T) {
	s, _ := newStorePath(t)
	c := newClock("2026-11-07T12:00:00Z")
	w := testWatcher(&fakeBoard{}, s, c)
	rows, err := s.Recent(state.Scope("p"), 1000)
	if err != nil {
		t.Fatalf("read rows: %v", err)
	}

	mixed := []board.Task{
		tickTask("t-dev", "p", "dev", "open"),
		tickTask("t-review", "p", "review", "open"),
	}
	recs, err := w.recommend(mixed, rows, c.Now())
	if err != nil {
		t.Fatalf("recommend: %v", err)
	}
	if len(recs) != 2 || recs[0].Task.Task != "t-review" || recs[1].Task.Task != "t-dev" {
		t.Fatalf("rank = %v, want review before dev", recs)
	}

	devOnly := []board.Task{
		tickTask("t-old", "p", "dev", "open"),
		tickTask("t-new", "p", "dev", "open"),
	}
	recs, err = w.recommend(devOnly, rows, c.Now())
	if err != nil {
		t.Fatalf("recommend dev only: %v", err)
	}
	if len(recs) != 2 || recs[0].Task.Task != "t-old" {
		t.Fatalf("dev rank = %v, want oldest first", recs)
	}

	text, err := state.Encode(state.Act{Task: "t-old", Project: "p", Kind: "dev", LaunchedAt: c.Now(), Observed: state.ObservedUnconfirmed})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(state.KindAct, state.Scope("p"), "act", text); err != nil {
		t.Fatalf("put act: %v", err)
	}
	rows, err = s.Recent(state.Scope("p"), 1000)
	if err != nil {
		t.Fatal(err)
	}
	recs, err = w.recommend(devOnly, rows, c.Now())
	if err != nil {
		t.Fatalf("recommend gated: %v", err)
	}
	if len(recs) != 1 || recs[0].Task.Task != "t-new" {
		t.Fatalf("gated rank = %v, want only t-new", recs)
	}
}

// AC8: two instructions inside 90 s launch once; the second names the earlier
// launch. After the gate, a new instruction may launch again.
func TestAC8TheInstructionGate(t *testing.T) {
	b := &fakeBoard{tasks: []board.Task{{Task: "t-1", Project: "p", Kind: "dev", State: "open", ProjectPath: "/tmp/p"}}}
	s, path := newStorePath(t)
	c := newClock("2026-11-07T12:00:00Z")
	seedKnowledge(t, path, "cap.dispatch: echo {task}\ncap.agent: fake")
	l := &fakeLauncher{}
	d := testDispatcher(b, s, l, c)

	if _, err := d.Act(ctx(), "t-1"); err != nil {
		t.Fatalf("first act: %v", err)
	}
	c.Advance(10 * time.Second)
	_, err := d.Act(ctx(), "t-1")
	if err == nil || !IsRefusal(err) {
		t.Fatalf("second act = %v, want refusal", err)
	}
	if !strings.Contains(err.Error(), "2026-11-07T12:00:00Z") {
		t.Fatalf("refusal does not name the earlier launch: %v", err)
	}
	if len(l.calls) != 1 {
		t.Fatalf("launches = %d, want 1 inside the gate", len(l.calls))
	}

	c.Advance(81 * time.Second)
	if _, err := d.Act(ctx(), "t-1"); err != nil {
		t.Fatalf("act after gate: %v", err)
	}
	if len(l.calls) != 2 {
		t.Fatalf("launches = %d, want 2 after the gate", len(l.calls))
	}
	if n := countOpen(t, s, state.Scope("p"), state.KindAct); n != 1 {
		t.Fatalf("open acts = %d, want exactly one", n)
	}
}
