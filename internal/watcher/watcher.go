// Package watcher is cap's two forms and one boundary. Watcher.Tick reads the
// board and never holds a launcher. Dispatcher.Act runs one named instruction.
// The clock path therefore cannot launch anything, by construction.
package watcher

import (
	"time"

	"github.com/IanLuo/cap/internal/board"
	"github.com/IanLuo/cap/internal/state"
)

const (
	// DefaultGate is the instruction gate: a task launched less than this ago
	// is not recommended or launched again.
	DefaultGate = 90 * time.Second
	// DefaultInterval is the watcher clock.
	DefaultInterval = 30 * time.Second

	// rowLimit and cursorLimit bound one memstore listing. A listing that is
	// too short can hide the open record behind closed history, so both match
	// fs's listing bound (1<<20), not a guess at today's volume.
	rowLimit       = 1 << 20
	cursorLimit    = 1 << 20
	knowledgeLimit = 256
)

// Trace is cap's own log: one JSON line per tick and per act. It never gates a
// decision, and cap never reads it.
type Trace interface {
	Write(event map[string]any)
}

// Launcher starts one command detached in a working directory.
type Launcher interface {
	Launch(dir, command string) error
}

// Watcher is the clock path. It has no Launcher field: a tick cannot launch.
type Watcher struct {
	Board board.Reader
	Store state.Store
	Trace Trace
	Now   func() time.Time
	Gate  time.Duration
}

// Dispatcher is the on-demand path: one task the user named, one command.
type Dispatcher struct {
	Board    board.Reader
	Store    state.Store
	Launcher Launcher
	Trace    Trace
	Now      func() time.Time
	Gate     time.Duration
}

func (w *Watcher) now() time.Time {
	if w.Now == nil {
		return time.Now()
	}
	return w.Now()
}

func (w *Watcher) gate() time.Duration {
	if w.Gate == 0 {
		return DefaultGate
	}
	return w.Gate
}

func (d *Dispatcher) now() time.Time {
	if d.Now == nil {
		return time.Now()
	}
	return d.Now()
}

func (d *Dispatcher) gate() time.Duration {
	if d.Gate == 0 {
		return DefaultGate
	}
	return d.Gate
}

func (w *Watcher) trace(event map[string]any) {
	if w.Trace != nil {
		w.Trace.Write(event)
	}
}

func (d *Dispatcher) trace(event map[string]any) {
	if d.Trace != nil {
		d.Trace.Write(event)
	}
}

// listRows returns one row listing per cap:<project> scope in view: every
// project on the board, plus every project the previous cursor knew, so a task
// that just closed is still reconciled.
func listRows(s state.Store, tasks []board.Task, cursor *state.Cursor) ([]state.Row, error) {
	scopes := map[string]bool{}
	for _, t := range tasks {
		scopes[state.Scope(t.Project)] = true
	}
	if cursor != nil {
		for _, t := range cursor.Tasks {
			scopes[state.Scope(t.Project)] = true
		}
	}
	all := make([]state.Row, 0)
	for scope := range scopes {
		rows, err := s.Recent(scope, rowLimit)
		if err != nil {
			return nil, err
		}
		all = append(all, rows...)
	}
	return all, nil
}
