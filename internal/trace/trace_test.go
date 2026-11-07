package trace

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestOneJSONLinePerEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.log")
	l := Open(path)
	l.Write(map[string]any{"kind": "tick", "changed": true})
	l.Write(map[string]any{"kind": "act", "task": "t-1", "result": "launched"})

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open trace: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var kinds []string
	for sc.Scan() {
		var event map[string]any
		if err := json.Unmarshal(sc.Bytes(), &event); err != nil {
			t.Fatalf("trace line is not JSON: %v", err)
		}
		kinds = append(kinds, event["kind"].(string))
	}
	if len(kinds) != 2 || kinds[0] != "tick" || kinds[1] != "act" {
		t.Fatalf("trace lines = %v, want one tick then one act", kinds)
	}
}

func TestUnusablePathDropsInsteadOfGating(t *testing.T) {
	l := Open("")
	l.Write(map[string]any{"kind": "tick"}) // must not panic or block
}
