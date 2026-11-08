package trace

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
)

// Tail reads at most n of the newest parsed events. A missing file is no
// events, not an error: the operator interface must work before the first
// tick. Unparseable lines are skipped.
func Tail(path string, n int) ([]map[string]any, error) {
	if n <= 0 {
		n = 50
	}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	var out []map[string]any
	for sc.Scan() {
		var event map[string]any
		if json.Unmarshal(sc.Bytes(), &event) != nil {
			continue
		}
		out = append(out, event)
		if len(out) > n {
			out = out[len(out)-n:]
		}
	}
	return out, sc.Err()
}
