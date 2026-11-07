// Package trace is cap's own log: one JSON line per tick and per act. It never
// gates a decision and cap never reads it.
package trace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

const maxBytes = 1 << 20

// Log appends JSON lines to one file, rotating once when it grows.
type Log struct {
	path string
	mu   sync.Mutex
}

// Open returns a log at path. An empty or unusable path yields a log that drops
// writes: the trace never gates a decision.
func Open(path string) *Log {
	if path == "" {
		return &Log{}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return &Log{}
	}
	return &Log{path: path}
}

func (l *Log) Write(event map[string]any) {
	if l == nil || l.path == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rotate()
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	_ = enc.Encode(event)
}

func (l *Log) rotate() {
	info, err := os.Stat(l.path)
	if err != nil || info.Size() < maxBytes {
		return
	}
	old := l.path + ".1"
	_ = os.Remove(old)
	_ = os.Rename(l.path, old)
}
