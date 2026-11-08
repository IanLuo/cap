package watcher

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IanLuo/cap/internal/board"
	"github.com/IanLuo/cap/internal/state"
)

// AC6 against the real fs client. The fake fs logs its argv, and the test
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

	rows, err := s.Recent(state.Scope("p"), 1000)
	if err != nil {
		t.Fatalf("read cap rows: %v", err)
	}
	for _, r := range rows {
		if strings.HasPrefix(r.Kind, "fs.") {
			t.Fatalf("cap wrote an fs row: %s", r.Kind)
		}
	}
}
