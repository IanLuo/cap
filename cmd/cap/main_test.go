package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fs")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write fake fs: %v", err)
	}
	return path
}

func TestTickCommandReportsBoardChanges(t *testing.T) {
	t.Setenv("MEMSTORE_DB", filepath.Join(t.TempDir(), "memory.db"))
	t.Setenv("CAP_TRACE", filepath.Join(t.TempDir(), "trace.log"))
	t.Setenv("CAP_FS_BIN", writeScript(t, `cat <<'JSON'
{"ok":true,"data":{"projects":[{"project":"p","tasks":[{"task":"t-1","kind":"dev","state":"open","goal":"g","checks":["c"]}]}]}}
JSON`))

	var out, errOut bytes.Buffer
	if code := run([]string{"tick"}, &out, &errOut); code != 0 {
		t.Fatalf("tick exit = %d, stderr %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "changes:") || !strings.Contains(out.String(), "t-1") {
		t.Fatalf("tick report = %q, want the new task named", out.String())
	}
}

func TestTickCommandFailsClosedOnAnUnreadableBoard(t *testing.T) {
	t.Setenv("MEMSTORE_DB", filepath.Join(t.TempDir(), "memory.db"))
	t.Setenv("CAP_TRACE", filepath.Join(t.TempDir(), "trace.log"))
	t.Setenv("CAP_FS_BIN", writeScript(t, `echo "store unavailable" >&2; exit 1`))

	var out, errOut bytes.Buffer
	if code := run([]string{"tick"}, &out, &errOut); code != 1 {
		t.Fatalf("tick exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "cap:") {
		t.Fatalf("stderr = %q, want the cause named", errOut.String())
	}
	if out.String() != "" {
		t.Fatalf("failed tick wrote a report: %q", out.String())
	}
}

func TestUsageRefusesAnUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"dispatch"}, &out, &errOut); code != 2 {
		t.Fatalf("unknown command exit = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "usage: cap") {
		t.Fatalf("stderr = %q, want usage", errOut.String())
	}
}
