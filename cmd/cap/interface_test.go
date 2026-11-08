package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IanLuo/memstore"
)

// writeExec writes an executable script.
func writeExec(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// fakeLaunchctl models one label. STATE holds the `print` body; bootstrap and
// kickstart create it, bootout removes it. Every call is logged.
func fakeLaunchctl(t *testing.T, state, argvLog string) string {
	t.Helper()
	return writeExec(t, "launchctl", fmt.Sprintf(`
echo "$@" >> %q
case "$1" in
  print)
    if [ -f %q ]; then cat %q; exit 0; fi
    echo "Could not find service" >&2
    exit 113
    ;;
  bootstrap) printf '\tpid = 5000\n' > %q; exit 0 ;;
  kickstart) if [ -f %q ]; then printf '\tpid = 5001\n' > %q; fi; exit 0 ;;
  bootout) rm -f %q; exit 0 ;;
esac
exit 0
`, argvLog, state, state, state, state, state, state))
}

func argvLogLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func TestStatusJSONReportsRunning(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(state, []byte("\tpid = 4242\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAP_LAUNCHCTL", fakeLaunchctl(t, state, filepath.Join(t.TempDir(), "argv.log")))
	t.Setenv("CAP_SERVICE_PLIST", filepath.Join(t.TempDir(), "cap.plist"))
	traceFile := filepath.Join(t.TempDir(), "trace.log")
	at := time.Now().Add(-10 * time.Second).UTC().Format(time.RFC3339Nano)
	if err := os.WriteFile(traceFile, []byte(`{"kind":"tick","at":"`+at+`","changed":false}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAP_TRACE", traceFile)

	var out, errOut bytes.Buffer
	if code := run([]string{"status", "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("status exit = %d, stderr %s", code, errOut.String())
	}
	var got statusJSON
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("status json: %v: %s", err, out.String())
	}
	if !got.Running || !got.Loaded || got.PID != 4242 {
		t.Fatalf("status = %+v, want running pid 4242", got)
	}
	if got.LastTickAt != at || got.Stale || got.LastError != "" {
		t.Fatalf("status = %+v, want the fresh tick and no error", got)
	}
}

func TestStatusCallsStoppedAtNoSuchService(t *testing.T) {
	t.Setenv("CAP_LAUNCHCTL", fakeLaunchctl(t, filepath.Join(t.TempDir(), "absent"), filepath.Join(t.TempDir(), "argv.log")))
	t.Setenv("CAP_TRACE", filepath.Join(t.TempDir(), "trace.log"))

	var out, errOut bytes.Buffer
	if code := run([]string{"status"}, &out, &errOut); code != 0 {
		t.Fatalf("status exit = %d, stderr %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "stopped") {
		t.Fatalf("status = %q, want stopped", out.String())
	}
}

func TestStartBootstrapsWhenStopped(t *testing.T) {
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")
	plist := filepath.Join(dir, "home-manager.cap.plist")
	if err := os.WriteFile(plist, []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAP_LAUNCHCTL", fakeLaunchctl(t, filepath.Join(dir, "state"), argvLog))
	t.Setenv("CAP_SERVICE_PLIST", plist)
	t.Setenv("CAP_SERVICE_LABEL", "home-manager.cap")
	t.Setenv("CAP_TRACE", filepath.Join(dir, "trace.log"))

	var out, errOut bytes.Buffer
	if code := run([]string{"start", "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("start exit = %d, stderr %s", code, errOut.String())
	}
	want := fmt.Sprintf("bootstrap gui/%d %s", os.Getuid(), plist)
	if !strings.Contains(strings.Join(argvLogLines(t, argvLog), "\n"), want) {
		t.Fatalf("argv = %v, want %q", argvLogLines(t, argvLog), want)
	}
	var got actionJSON
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("start json: %v: %s", err, out.String())
	}
	if got.Action != "start" || !got.Running || got.PID != 5000 {
		t.Fatalf("start = %+v, want running pid 5000", got)
	}
}

func TestStartLeavesRunningWatcherAlone(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	if err := os.WriteFile(state, []byte("\tpid = 4242\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	argvLog := filepath.Join(dir, "argv.log")
	t.Setenv("CAP_LAUNCHCTL", fakeLaunchctl(t, state, argvLog))
	t.Setenv("CAP_TRACE", filepath.Join(dir, "trace.log"))

	var out, errOut bytes.Buffer
	if code := run([]string{"start", "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("start exit = %d, stderr %s", code, errOut.String())
	}
	for _, line := range argvLogLines(t, argvLog) {
		if strings.HasPrefix(line, "kickstart") || strings.HasPrefix(line, "bootstrap") {
			t.Fatalf("start restarted a running watcher: %v", argvLogLines(t, argvLog))
		}
	}
	if !strings.Contains(out.String(), `"pid":4242`) {
		t.Fatalf("start = %s, want the existing pid", out.String())
	}
}

func TestStartKicksLoadedWatcherWithoutPID(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	if err := os.WriteFile(state, []byte("\tstate = running\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	argvLog := filepath.Join(dir, "argv.log")
	t.Setenv("CAP_LAUNCHCTL", fakeLaunchctl(t, state, argvLog))
	t.Setenv("CAP_TRACE", filepath.Join(dir, "trace.log"))

	var out, errOut bytes.Buffer
	if code := run([]string{"start"}, &out, &errOut); code != 0 {
		t.Fatalf("start exit = %d, stderr %s", code, errOut.String())
	}
	want := fmt.Sprintf("kickstart -k gui/%d/home-manager.cap", os.Getuid())
	if !strings.Contains(strings.Join(argvLogLines(t, argvLog), "\n"), want) {
		t.Fatalf("argv = %v, want %q", argvLogLines(t, argvLog), want)
	}
}

func TestStopBootsOut(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	if err := os.WriteFile(state, []byte("\tpid = 4242\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	argvLog := filepath.Join(dir, "argv.log")
	t.Setenv("CAP_LAUNCHCTL", fakeLaunchctl(t, state, argvLog))
	t.Setenv("CAP_TRACE", filepath.Join(dir, "trace.log"))

	var out, errOut bytes.Buffer
	if code := run([]string{"stop", "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("stop exit = %d, stderr %s", code, errOut.String())
	}
	want := fmt.Sprintf("bootout gui/%d/home-manager.cap", os.Getuid())
	if !strings.Contains(strings.Join(argvLogLines(t, argvLog), "\n"), want) {
		t.Fatalf("argv = %v, want %q", argvLogLines(t, argvLog), want)
	}
	if !strings.Contains(out.String(), `"running":false`) {
		t.Fatalf("stop = %s, want running false", out.String())
	}
}

// AC12: every interface verb leaves the board and the store untouched. The
// fake fs logs argv; the memstore file must never be created.
func TestInterfaceTouchesNoBoardOrStore(t *testing.T) {
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "fs-argv.log")
	fsBin := writeExec(t, "fs", fmt.Sprintf(`echo "$@" >> %q; exit 0`, argvLog))
	t.Setenv("CAP_FS_BIN", fsBin)
	store := filepath.Join(dir, "memory.db")
	t.Setenv("MEMSTORE_DB", store)
	t.Setenv("CAP_TRACE", filepath.Join(dir, "trace.log"))
	t.Setenv("CAP_LAUNCHCTL", fakeLaunchctl(t, filepath.Join(dir, "state"), filepath.Join(dir, "lc.log")))
	plist := filepath.Join(dir, "cap.plist")
	if err := os.WriteFile(plist, []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAP_SERVICE_PLIST", plist)

	for _, args := range [][]string{
		{"status"}, {"start"}, {"stop"}, {"logs", "--tail", "3"},
	} {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != 0 {
			t.Fatalf("%v exit = %d, stderr %s", args, code, errOut.String())
		}
	}
	if _, err := os.Stat(argvLog); !os.IsNotExist(err) {
		t.Fatalf("fs was called by the interface: %v", argvLogLines(t, argvLog))
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Fatalf("the interface opened the store: %v", err)
	}
}

func TestLogsTailJSON(t *testing.T) {
	traceFile := filepath.Join(t.TempDir(), "trace.log")
	body := `{"kind":"tick","n":1}
{"kind":"act","n":2}
{"kind":"tick","n":3}
`
	if err := os.WriteFile(traceFile, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAP_TRACE", traceFile)

	var out, errOut bytes.Buffer
	if code := run([]string{"logs", "--tail", "2", "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("logs exit = %d, stderr %s", code, errOut.String())
	}
	var got logsJSON
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("logs json: %v: %s", err, out.String())
	}
	if len(got.Lines) != 2 || got.Lines[0]["n"].(float64) != 2 || got.Lines[1]["n"].(float64) != 3 {
		t.Fatalf("lines = %v, want the last two", got.Lines)
	}
}

func TestTickJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MEMSTORE_DB", filepath.Join(dir, "memory.db"))
	t.Setenv("CAP_TRACE", filepath.Join(dir, "trace.log"))
	t.Setenv("CAP_FS_BIN", writeExec(t, "fs", `cat <<'JSON'
{"ok":true,"data":{"projects":[{"project":"p","tasks":[{"task":"t-1","kind":"dev","state":"open","goal":"g","checks":["c"]}]}]}}
JSON`))

	var out, errOut bytes.Buffer
	if code := run([]string{"tick", "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("tick exit = %d, stderr %s", code, errOut.String())
	}
	var got reportJSON
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("tick json: %v: %s", err, out.String())
	}
	if !got.Changed || len(got.Changes) != 1 || got.Changes[0].Task != "t-1" {
		t.Fatalf("tick = %+v, want the t-1 change", got)
	}
	if got.Recommendations == nil || got.Confirmed == nil || got.Pending == nil {
		t.Fatalf("tick = %+v, want empty arrays not null", got)
	}
}

func TestActJSON(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "memory.db")
	t.Setenv("MEMSTORE_DB", dbPath)
	t.Setenv("CAP_TRACE", filepath.Join(dir, "trace.log"))
	t.Setenv("CAP_FS_BIN", writeExec(t, "fs", `cat <<'JSON'
{"ok":true,"data":{"projects":[{"project":"p","tasks":[{"task":"t-1","kind":"dev","state":"open","goal":"g","checks":["c"],"project_path":"/tmp"}]}]}}
JSON`))
	db, err := memstore.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Put(&memstore.Memory{Kind: "knowledge", Scope: "cap:cap", Text: "cap.dispatch: echo {task}\ncap.agent: fake"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if code := run([]string{"act", "--json", "t-1"}, &out, &errOut); code != 0 {
		t.Fatalf("act exit = %d, stderr %s", code, errOut.String())
	}
	var got actJSON
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("act json: %v: %s", err, out.String())
	}
	if !got.Launched || got.Command != "echo t-1" || got.Agent != "fake" || got.Task != "t-1" {
		t.Fatalf("act = %+v, want the fake command launched", got)
	}
}

func TestUnknownOptionIsUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"tick", "--bogus"}, &out, &errOut); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "usage: cap") {
		t.Fatalf("stderr = %q, want usage", errOut.String())
	}
}
