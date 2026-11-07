// Package state is cap's own memory: the three record kinds it owns, and the
// only place that talks to memstore. It refuses any scope that is not cap's, so
// the fs board can never be read through this package.
package state

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/IanLuo/memstore"
)

const (
	KindAct      = "cap.act"
	KindQuestion = "cap.question"
	KindCursor   = "cap.cursor"

	// KnowledgeKind is the only foreign kind cap reads. Knowledge is how to
	// start a worker; it is never a task row.
	KnowledgeKind = "knowledge"

	// CursorScope is the board-wide cursor's scope. Every other record is in
	// cap:<project>; the cursor covers every project, so it belongs to cap.
	CursorScope = "cap:cap"
)

// Scope is one project's cap state.
func Scope(project string) string { return "cap:" + project }

// Row is one memstore memory as cap reads it. Closed rows are history; open
// rows are the state a decision may use.
type Row struct {
	ID     int64
	Kind   string
	Scope  string
	Title  string
	Text   string
	Closed bool
}

// Store is cap's store seam. The methods are deliberately narrow:
//   - Recent only accepts a cap: scope, so fs rows are unreachable.
//   - SearchKnowledge only returns kind knowledge, so a task row can never be
//     mistaken for a fact about how to dispatch.
type Store interface {
	Put(kind, scope, title, text string) (int64, error)
	Recent(scope string, k int) ([]Row, error)
	SearchKnowledge(query string, k int) ([]Row, error)
	Close(id int64) error
}

// MemStore is the real store: the user's memstore file, never a second database.
type MemStore struct {
	db *memstore.Store
}

// Open opens the shared memstore file: path if given, else $MEMSTORE_DB, else
// memstore's own home default.
func Open(path string) (*MemStore, error) {
	if path == "" {
		p, err := DefaultPath()
		if err != nil {
			return nil, err
		}
		path = p
	}
	db, err := memstore.Open(path)
	if err != nil {
		return nil, err
	}
	return &MemStore{db: db}, nil
}

// DefaultPath is memstore's rule, not a second one cap invents.
func DefaultPath() (string, error) {
	if p := os.Getenv("MEMSTORE_DB"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no memstore path: MEMSTORE_DB is unset and the home directory is unknown: %w", err)
	}
	return filepath.Join(home, ".memstore", "memory.db"), nil
}

func (m *MemStore) CloseDB() error { return m.db.Close() }

func (m *MemStore) Put(kind, scope, title, text string) (int64, error) {
	if !strings.HasPrefix(scope, "cap:") {
		return 0, fmt.Errorf("cap writes only cap: scopes, got %q", scope)
	}
	if kind != KindAct && kind != KindQuestion && kind != KindCursor {
		return 0, fmt.Errorf("cap owns three kinds, not %q", kind)
	}
	return m.db.Put(&memstore.Memory{Kind: kind, Scope: scope, Title: title, Text: text, Origin: "cap"})
}

func (m *MemStore) Recent(scope string, k int) ([]Row, error) {
	if !strings.HasPrefix(scope, "cap:") {
		return nil, fmt.Errorf("cap reads only cap: scopes, got %q", scope)
	}
	mems, err := m.db.Recent(scope, k)
	if err != nil {
		return nil, err
	}
	return rows(mems), nil
}

func (m *MemStore) SearchKnowledge(query string, k int) ([]Row, error) {
	mems, err := m.db.Search(query, k)
	if err != nil {
		return nil, err
	}
	out := make([]Row, 0, len(mems))
	for _, mem := range mems {
		if mem.Kind == KnowledgeKind && mem.Status != "closed" {
			out = append(out, row(mem))
		}
	}
	return out, nil
}

func (m *MemStore) Close(id int64) error { return m.db.CloseMemory(id, nil) }

func rows(mems []memstore.Memory) []Row {
	out := make([]Row, 0, len(mems))
	for _, mem := range mems {
		out = append(out, row(mem))
	}
	return out
}

func row(mem memstore.Memory) Row {
	return Row{
		ID:     mem.Id,
		Kind:   mem.Kind,
		Scope:  mem.Scope,
		Title:  mem.Title,
		Text:   mem.Text,
		Closed: mem.Status == "closed",
	}
}
