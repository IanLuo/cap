package watcher

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/IanLuo/cap/internal/board"
	"github.com/IanLuo/cap/internal/state"
	"github.com/IanLuo/memstore"
)

type launchCall struct {
	Dir     string
	Command string
}

type fakeLauncher struct {
	calls []launchCall
	err   error
}

func (f *fakeLauncher) Launch(dir, command string) error {
	f.calls = append(f.calls, launchCall{Dir: dir, Command: command})
	return f.err
}

type captureTrace struct{ events []map[string]any }

func (c *captureTrace) Write(event map[string]any) { c.events = append(c.events, event) }

func newStorePath(t *testing.T) (*state.MemStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "memory.db")
	m, err := state.Open(path)
	if err != nil {
		t.Fatalf("open temp store: %v", err)
	}
	t.Cleanup(func() { _ = m.CloseDB() })
	return m, path
}

// seedKnowledge writes a knowledge record directly through memstore. cap's own
// store seam refuses to write knowledge, because cap never edits knowledge.
func seedKnowledge(t *testing.T, path, text string) int64 {
	t.Helper()
	db, err := memstore.Open(path)
	if err != nil {
		t.Fatalf("open store for knowledge: %v", err)
	}
	defer db.Close()
	id, err := db.Put(&memstore.Memory{Kind: state.KnowledgeKind, Scope: "fs:test", Text: text, Origin: "test"})
	if err != nil {
		t.Fatalf("seed knowledge: %v", err)
	}
	return id
}

func openRows(t *testing.T, s *state.MemStore, project string) []state.Row {
	t.Helper()
	rows, err := s.Recent(state.Scope(project), 1000)
	if err != nil {
		t.Fatalf("read %s: %v", project, err)
	}
	var out []state.Row
	for _, r := range rows {
		if !r.Closed {
			out = append(out, r)
		}
	}
	return out
}

func countOpen(t *testing.T, s *state.MemStore, scope, kind string) int {
	t.Helper()
	rows, err := s.Recent(scope, 1000)
	if err != nil {
		t.Fatalf("read %s: %v", scope, err)
	}
	n := 0
	for _, r := range rows {
		if r.Kind == kind && !r.Closed {
			n++
		}
	}
	return n
}

func openAct(t *testing.T, s *state.MemStore, project, task string) state.Act {
	t.Helper()
	for _, r := range openRows(t, s, project) {
		if r.Kind != state.KindAct {
			continue
		}
		a, err := state.DecodeAct(r)
		if err != nil {
			t.Fatalf("decode act: %v", err)
		}
		if a.Task == task {
			return a
		}
	}
	t.Fatalf("no open act for %s", task)
	return state.Act{}
}

type clock struct{ at time.Time }

func newClock(s string) *clock {
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return &clock{at: ts}
}

func (c *clock) Now() time.Time          { return c.at }
func (c *clock) Advance(d time.Duration) { c.at = c.at.Add(d) }

func tickTask(task, project, kind, stateName string) board.Task {
	return board.Task{Task: task, Project: project, Kind: kind, State: stateName}
}

func testWatcher(b board.Reader, s state.Store, c *clock) *Watcher {
	return &Watcher{Board: b, Store: s, Now: c.Now, Gate: 90 * time.Second}
}

func testDispatcher(b board.Reader, s state.Store, l Launcher, c *clock) *Dispatcher {
	return &Dispatcher{Board: b, Store: s, Launcher: l, Now: c.Now, Gate: 90 * time.Second}
}
