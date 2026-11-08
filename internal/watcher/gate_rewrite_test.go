package watcher

import (
	"strings"
	"testing"
	"time"

	"github.com/IanLuo/cap/internal/board"
	"github.com/IanLuo/cap/internal/state"
)

// The gate reads the newest act's launched_at, closed rows included. This test
// leaves only a closed act for the task (no open row at all), then instructs
// 25 s later. The gate must still refuse. Reading open rows alone would miss
// it and launch.
func TestGateCountsClosedActs(t *testing.T) {
	b := &fakeBoard{tasks: []board.Task{{Task: "t-1", Project: "p", Kind: "dev", State: "open", ProjectPath: "/tmp/p"}}}
	s, path := newStorePath(t)
	c := newClock("2026-11-08T12:00:00Z")
	seedKnowledge(t, path, "cap.dispatch: echo {task}\ncap.agent: fake")
	l := &fakeLauncher{}
	d := testDispatcher(b, s, l, c)

	if _, err := d.Act(ctx(), "t-1"); err != nil {
		t.Fatalf("act: %v", err)
	}
	a := openAct(t, s, "p", "t-1")
	// Close the only act, as a later rewrite or an external cleanup could.
	if err := s.Close(actRowID(t, s, "p", "t-1")); err != nil {
		t.Fatalf("close act: %v", err)
	}
	if n := countOpen(t, s, state.Scope("p"), state.KindAct); n != 0 {
		t.Fatalf("open acts = %d, want 0 so only the closed row remains", n)
	}

	c.Advance(25 * time.Second)
	_, err := d.Act(ctx(), "t-1")
	if err == nil || !IsRefusal(err) {
		t.Fatalf("instruction 25 s after a closed-only launch = %v, want gate refusal", err)
	}
	if !strings.Contains(err.Error(), a.LaunchedAt.UTC().Format("2006-01-02T15:04:05Z")) {
		t.Fatalf("refusal does not name the launch at %s: %v", a.LaunchedAt, err)
	}
	if len(l.calls) != 1 {
		t.Fatalf("launches = %d, want 1", len(l.calls))
	}
}

// actRowID returns the id of the one act row for a task, open or closed.
func actRowID(t *testing.T, s *state.MemStore, project, task string) int64 {
	t.Helper()
	rows, err := s.Recent(state.Scope(project), rowLimit)
	if err != nil {
		t.Fatalf("read %s: %v", project, err)
	}
	for _, r := range rows {
		if r.Kind != state.KindAct {
			continue
		}
		a, err := state.DecodeAct(r)
		if err != nil {
			t.Fatalf("decode act: %v", err)
		}
		if a.Task == task {
			return r.ID
		}
	}
	t.Fatalf("no act row for %s", task)
	return 0
}
