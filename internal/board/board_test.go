package board

import (
	"strings"
	"testing"
)

func TestDecodeFlattensProjectsInBoardOrder(t *testing.T) {
	out := []byte(`{
	  "ok": true,
	  "data": {"projects": [
	    {"project": "alpha", "tasks": [
	      {"task": "t-1", "kind": "dev", "state": "open", "goal": "one", "checks": ["go test ./..."]},
	      {"task": "t-2", "kind": "review", "state": "claimed", "goal": "two", "checks": []}
	    ]},
	    {"project": "beta", "tasks": []}
	  ]}
	}`)
	tasks, err := Decode(out)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("tasks = %d, want 2", len(tasks))
	}
	if tasks[0].Project != "alpha" || tasks[0].Task != "t-1" || tasks[1].Task != "t-2" {
		t.Fatalf("flatten = %+v, want alpha/t-1 then alpha/t-2", tasks)
	}
}

func TestDecodeCarriesTheCallerPaneAndTheDigest(t *testing.T) {
	out := []byte(`{
	  "ok": true,
	  "data": {"projects": [
	    {"project": "alpha", "tasks": [
	      {"task": "t-1", "kind": "dev", "state": "done", "goal": "one", "checks": [],
	       "process": {"status": "gone", "pane": "p1", "caller_pane": "p9"},
	       "evidence": [{"state": "dev-done", "role": "dev", "digest": "abc1234"}]}
	    ]}
	  ]}
	}`)
	tasks, err := Decode(out)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if tasks[0].Process.Pane != "p1" || tasks[0].Process.CallerPane != "p9" {
		t.Fatalf("process = %+v, want the launched pane and the caller pane", tasks[0].Process)
	}
	if len(tasks[0].Evidence) != 1 || tasks[0].Evidence[0].Digest != "abc1234" {
		t.Fatalf("evidence = %+v, want the dev-done digest", tasks[0].Evidence)
	}
}

func TestDecodeRefusesARefusal(t *testing.T) {
	if _, err := Decode([]byte(`{"ok": false, "error": "store unavailable"}`)); err == nil ||
		!strings.Contains(err.Error(), "store unavailable") {
		t.Fatalf("refusal decode = %v, want the refusal named", err)
	}
}

func TestDecodeRefusesMalformed(t *testing.T) {
	if _, err := Decode([]byte(`{"ok": true, "data":`)); err == nil {
		t.Fatal("malformed status decoded without error")
	}
}
