package watcher

import (
	"context"
	"testing"

	"github.com/IanLuo/cap/internal/board"
	"github.com/IanLuo/cap/internal/state"
)

// fakeBoard is the fs status boundary: the tick may call Status once and
// nothing else. It never claims, reports or writes a task.
type fakeBoard struct {
	tasks []board.Task
	calls int
	err   error
}

func (f *fakeBoard) Status(context.Context) ([]board.Task, error) {
	f.calls++
	return f.tasks, f.err
}

// AC1: one read, no act on a tick. Two projects with open tasks, one tick:
// exactly one fs status call and no command launched. The Watcher type has no
// launcher at all, so the only way this test can pass is the tick path staying
// off the dispatch path.
func TestTickReadsTheBoardOnceAndLaunchesNothing(t *testing.T) {
	b := &fakeBoard{tasks: []board.Task{
		{Task: "t-alpha", Project: "alpha", Kind: "dev", State: "open"},
		{Task: "t-beta", Project: "beta", Kind: "review", State: "open"},
	}}
	s, _ := newStorePath(t)
	w := testWatcher(b, s, newClock("2026-11-07T12:00:00Z"))

	if _, err := w.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if b.calls != 1 {
		t.Fatalf("fs status calls = %d, want exactly 1", b.calls)
	}
	for _, project := range []string{"alpha", "beta"} {
		rows, err := s.Recent(state.Scope(project), 100)
		if err != nil {
			t.Fatalf("read %s: %v", project, err)
		}
		for _, r := range rows {
			if r.Kind == state.KindAct || r.Kind == state.KindQuestion {
				t.Fatalf("tick wrote %s in %s; the tick must not act", r.Kind, project)
			}
		}
	}
}
