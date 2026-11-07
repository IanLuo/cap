package state

import (
	"path/filepath"
	"testing"

	"github.com/IanLuo/memstore"
)

func TestStoreRefusesForeignScopesAndKinds(t *testing.T) {
	m, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer m.CloseDB()

	if _, err := m.Put(KindAct, "fs:p", "t", "text"); err == nil {
		t.Fatal("Put accepted an fs scope")
	}
	if _, err := m.Put("fs.task", Scope("p"), "t", "text"); err == nil {
		t.Fatal("Put accepted a foreign kind")
	}
	if _, err := m.Recent("fs:p", 10); err == nil {
		t.Fatal("Recent accepted an fs scope")
	}
	if _, err := m.Put(KindAct, Scope("p"), "t", `{"task":"t-1"}`); err != nil {
		t.Fatalf("Put refused a cap record: %v", err)
	}
}

func TestSearchKnowledgeNeverReturnsATaskRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	m, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer m.CloseDB()

	db, err := memstore.Open(path)
	if err != nil {
		t.Fatalf("open direct: %v", err)
	}
	defer db.Close()
	if _, err := db.Put(&memstore.Memory{Kind: KnowledgeKind, Scope: "fs:test", Text: "cap.dispatch: echo hi"}); err != nil {
		t.Fatalf("seed knowledge: %v", err)
	}
	if _, err := db.Put(&memstore.Memory{Kind: "fs.task", Scope: "fs:p", Text: "cap.dispatch: echo wrong"}); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	rows, err := m.SearchKnowledge("cap.dispatch", 20)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(rows) != 1 || rows[0].Kind != KnowledgeKind {
		t.Fatalf("search rows = %+v, want exactly the knowledge row", rows)
	}
}
