package watcher

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IanLuo/cap/internal/board"
	"github.com/IanLuo/memstore"
)

// AC6 against the real board client. The fake fs is a shell stub that logs its
// argv; it is not the real fs binary. The test
// asserts that across a tick and an act every call was `fs status`: no add,
// claim, report, or done. The board answer is also identical before and after.
func TestAC6OnlyStatusReachesFS(t *testing.T) {
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")
	fsBin := filepath.Join(dir, "fs")
	script := `#!/bin/sh
echo "$@" >> "` + argvLog + `"
cat <<'JSON'
{"ok":true,"data":{"projects":[{"project":"p","tasks":[{"task":"t-1","kind":"dev","state":"open","goal":"g","checks":["true"],"project_path":"/tmp/p"}]}]}}
JSON
`
	if err := os.WriteFile(fsBin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake fs: %v", err)
	}
	client := board.Client{Bin: fsBin}
	before, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("first status: %v", err)
	}

	s, path := newStorePath(t)
	seedKnowledge(t, path, "cap.dispatch: echo {task}\ncap.agent: fake")

	// The fs scope is fs's own. Count its rows through a second handle before
	// and after cap ran: cap must add none. Reading cap's own scope would prove
	// nothing, because fs rows never live there.
	beforeRows := rowsInScope(t, path, "fs:p")

	c := newClock("2026-11-08T12:00:00Z")
	if _, err := testWatcher(client, s, c).Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if _, err := testDispatcher(client, s, &fakeLauncher{}, c).Act(context.Background(), "t-1"); err != nil {
		t.Fatalf("act: %v", err)
	}

	after, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("second status: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("board answer changed across cap: before %+v after %+v", before, after)
	}

	raw, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatalf("read argv log: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if !strings.HasPrefix(line, "status ") {
			t.Fatalf("cap invoked fs with %q; only status is allowed", line)
		}
	}

	// The count is unchanged, so cap added no fs rows.
	if afterRows := rowsInScope(t, path, "fs:p"); afterRows != beforeRows {
		t.Fatalf("cap changed fs:p rows from %d to %d; fs rows must be untouched", beforeRows, afterRows)
	}
}

// rowsInScope counts live rows in one scope through its own memstore handle,
// so the count does not depend on cap's state layer.
func rowsInScope(t *testing.T, path, scope string) int {
	t.Helper()
	db, err := memstore.Open(path)
	if err != nil {
		t.Fatalf("open store to count %s: %v", scope, err)
	}
	defer db.Close()
	mems, err := db.Recent(scope, 1<<20)
	if err != nil {
		t.Fatalf("count %s: %v", scope, err)
	}
	return len(mems)
}
