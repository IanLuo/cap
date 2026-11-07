package watcher

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/IanLuo/cap/internal/board"
	"github.com/IanLuo/cap/internal/state"
)

// spyBoard and spyStore record every external call. A call is proof that the
// clock path reached something; a forbidden name is proof it reached the wrong
// thing.
type spyBoard struct {
	inner *fakeBoard
	calls []string
}

func (s *spyBoard) Status(c context.Context) ([]board.Task, error) {
	s.calls = append(s.calls, "status")
	return s.inner.Status(c)
}

type spyStore struct {
	state.Store
	calls []string
}

func (s *spyStore) Put(kind, scope, title, text string) (int64, error) {
	s.calls = append(s.calls, "put")
	return s.Store.Put(kind, scope, title, text)
}

func (s *spyStore) Recent(scope string, k int) ([]state.Row, error) {
	s.calls = append(s.calls, "recent")
	return s.Store.Recent(scope, k)
}

func (s *spyStore) SearchKnowledge(query string, k int) ([]state.Row, error) {
	s.calls = append(s.calls, "search-knowledge")
	return s.Store.SearchKnowledge(query, k)
}

func (s *spyStore) Close(id int64) error {
	s.calls = append(s.calls, "close")
	return s.Store.Close(id)
}

func forbidden(call string) bool {
	for _, bad := range []string{"model", "agent", "pane", "diff", "transcript", "claim", "report"} {
		if strings.Contains(call, bad) {
			return true
		}
	}
	return false
}

// AC9 + N3: a simulated day of ticks (2880 at 30 s) makes no model call, reads
// nothing about the work, and returns text. The only calls are the board read
// and cap's own state.
func TestAC9ZeroTokensOnTheClock(t *testing.T) {
	base := &fakeBoard{tasks: []board.Task{tickTask("t-1", "p", "dev", "open")}}
	b := &spyBoard{inner: base}
	inner, _ := newStorePath(t)
	s := &spyStore{Store: inner}
	c := newClock("2026-11-07T00:00:00Z")
	w := testWatcher(b, s, c)

	var reports []string
	for i := 0; i < 2880; i++ {
		report, err := w.Tick(context.Background())
		if err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		reports = append(reports, report.String())
		c.Advance(30 * time.Second)
	}
	if len(b.calls) != 2880 {
		t.Fatalf("board calls = %d, want one per tick", len(b.calls))
	}
	for _, call := range append(append([]string{}, b.calls...), s.calls...) {
		if forbidden(call) {
			t.Fatalf("clock path made a forbidden call %q", call)
		}
	}
	if reports[0] == "" {
		t.Fatal("first tick reported nothing about a board it had never seen")
	}
	for _, r := range reports[1:] {
		if r != "" {
			t.Fatalf("unchanged tick reported %q; nothing should be reported", r)
		}
	}
}

// AC10: an unreadable board or store fails the tick closed: non-zero by the
// caller's contract, no cursor write, no act; an unreadable board, store, or
// command on an instruction means no act record.
func TestAC10FailClosed(t *testing.T) {
	t.Run("board", func(t *testing.T) {
		s, _ := newStorePath(t)
		w := testWatcher(&fakeBoard{err: errors.New("board unavailable")}, s, newClock("2026-11-07T12:00:00Z"))
		if _, err := w.Tick(ctx()); err == nil {
			t.Fatal("tick with unreadable board returned nil error")
		}
		if n := countOpen(t, s, state.CursorScope, state.KindCursor); n != 0 {
			t.Fatalf("unreadable board wrote %d cursors", n)
		}
	})
	t.Run("store", func(t *testing.T) {
		w := testWatcher(&fakeBoard{tasks: []board.Task{tickTask("t-1", "p", "dev", "open")}}, failingStore{errors.New("store unavailable")}, newClock("2026-11-07T12:00:00Z"))
		if _, err := w.Tick(ctx()); err == nil {
			t.Fatal("tick with unreadable store returned nil error")
		}
	})
	t.Run("open store", func(t *testing.T) {
		if _, err := state.Open(t.TempDir()); err == nil {
			t.Fatal("opening a directory as a store returned nil error")
		}
	})
	t.Run("launch", func(t *testing.T) {
		b := &fakeBoard{tasks: []board.Task{{Task: "t-1", Project: "p", Kind: "dev", State: "open", ProjectPath: "/tmp/p"}}}
		s, path := newStorePath(t)
		seedKnowledge(t, path, "cap.dispatch: echo {task}\ncap.agent: fake")
		c := newClock("2026-11-07T12:00:00Z")
		d := testDispatcher(b, s, &fakeLauncher{err: errors.New("cannot start")}, c)
		if _, err := d.Act(ctx(), "t-1"); err == nil {
			t.Fatal("failed launch returned nil error")
		}
		if n := countOpen(t, s, state.Scope("p"), state.KindAct); n != 0 {
			t.Fatalf("failed launch wrote %d acts", n)
		}
	})
}

type failingStore struct{ err error }

func (f failingStore) Put(string, string, string, string) (int64, error) { return 0, f.err }
func (f failingStore) Recent(string, int) ([]state.Row, error)           { return nil, f.err }
func (f failingStore) SearchKnowledge(string, int) ([]state.Row, error)  { return nil, f.err }
func (f failingStore) Close(int64) error                                 { return f.err }

// N1: a tick returns in milliseconds against a large board.
func TestN1TickStaysFast(t *testing.T) {
	var tasks []board.Task
	for p := 0; p < 10; p++ {
		for i := 0; i < 20; i++ {
			tasks = append(tasks, tickTask(fmt.Sprintf("t-%d-%d", p, i), fmt.Sprintf("p%d", p), "dev", "open"))
		}
	}
	s, _ := newStorePath(t)
	w := testWatcher(&fakeBoard{tasks: tasks}, s, newClock("2026-11-07T12:00:00Z"))
	start := time.Now()
	if _, err := w.Tick(ctx()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("tick took %s; the clock path must return in milliseconds", d)
	}
}

// N4: ten unchanged ticks leave the row count flat; the cursor is written once.
func TestN4BoundedState(t *testing.T) {
	b := &fakeBoard{tasks: []board.Task{tickTask("t-1", "p", "dev", "open")}}
	s, _ := newStorePath(t)
	c := newClock("2026-11-07T12:00:00Z")
	w := testWatcher(b, s, c)
	if _, err := w.Tick(ctx()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	count := func() int {
		total := 0
		for _, scope := range []string{state.CursorScope, state.Scope("p")} {
			rows, err := s.Recent(scope, 1000)
			if err != nil {
				t.Fatalf("read %s: %v", scope, err)
			}
			total += len(rows)
		}
		return total
	}
	before := count()
	for i := 0; i < 10; i++ {
		c.Advance(30 * time.Second)
		if _, err := w.Tick(ctx()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if after := count(); after != before {
		t.Fatalf("ten unchanged ticks grew rows %d -> %d", before, after)
	}
}
