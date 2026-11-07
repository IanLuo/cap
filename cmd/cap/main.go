// Command cap is the coordinator's watcher and advisor. `cap tick` reads the
// board and never launches. `cap act <task>` runs one instruction. `cap watch`
// repeats the tick on the 30 s clock.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/IanLuo/cap/internal/board"
	"github.com/IanLuo/cap/internal/launch"
	"github.com/IanLuo/cap/internal/state"
	"github.com/IanLuo/cap/internal/trace"
	"github.com/IanLuo/cap/internal/watcher"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "tick":
		if len(args) != 1 {
			usage(stderr)
			return 2
		}
		return tickCmd(stdout, stderr)
	case "watch":
		if len(args) != 1 {
			usage(stderr)
			return 2
		}
		return watchCmd(stdout, stderr)
	case "act":
		if len(args) != 2 {
			usage(stderr)
			return 2
		}
		return actCmd(args[1], stdout, stderr)
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	default:
		usage(stderr)
		return 2
	}
}

func tickCmd(stdout, stderr io.Writer) int {
	store, log, err := open()
	if err != nil {
		fmt.Fprintf(stderr, "cap: %v\n", err)
		return 1
	}
	defer store.CloseDB()

	w := &watcher.Watcher{Board: board.Client{Bin: fsBin()}, Store: store, Trace: log}
	report, err := w.Tick(context.Background())
	if err != nil {
		log.Write(map[string]any{"kind": "tick", "at": time.Now().UTC().Format(time.RFC3339Nano), "result": "error", "error": err.Error()})
		fmt.Fprintf(stderr, "cap: %v\n", err)
		return 1
	}
	fmt.Fprint(stdout, report.String())
	return 0
}

func watchCmd(stdout, stderr io.Writer) int {
	store, log, err := open()
	if err != nil {
		fmt.Fprintf(stderr, "cap: %v\n", err)
		return 1
	}
	defer store.CloseDB()

	w := &watcher.Watcher{Board: board.Client{Bin: fsBin()}, Store: store, Trace: log}
	ticker := time.NewTicker(watcher.DefaultInterval)
	defer ticker.Stop()
	for {
		report, err := w.Tick(context.Background())
		if err != nil {
			log.Write(map[string]any{"kind": "tick", "at": time.Now().UTC().Format(time.RFC3339Nano), "result": "error", "error": err.Error()})
			fmt.Fprintf(stderr, "cap: %v\n", err)
		} else {
			fmt.Fprint(stdout, report.String())
		}
		<-ticker.C
	}
}

func actCmd(tid string, stdout, stderr io.Writer) int {
	store, log, err := open()
	if err != nil {
		fmt.Fprintf(stderr, "cap: %v\n", err)
		return 1
	}
	defer store.CloseDB()

	d := &watcher.Dispatcher{Board: board.Client{Bin: fsBin()}, Store: store, Launcher: launch.Detached{}, Trace: log}
	result, err := d.Act(context.Background(), tid)
	if err != nil {
		fmt.Fprintf(stderr, "cap: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "launched %s for %s (knowledge %s, agent %s)\n", result.Command, result.Task, result.Knowledge, result.Agent)
	return 0
}

func open() (*state.MemStore, *trace.Log, error) {
	store, err := state.Open("")
	if err != nil {
		return nil, nil, err
	}
	return store, trace.Open(tracePath()), nil
}

func fsBin() string {
	if v := os.Getenv("CAP_FS_BIN"); v != "" {
		return v
	}
	return "fs"
}

func tracePath() string {
	if v := os.Getenv("CAP_TRACE"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cap", "trace.log")
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage: cap <command>

  cap tick          one board read: report changes and the ranked next tasks
  cap watch         repeat the tick every 30 s (no model, no launcher)
  cap act <task>    run one instruction for a named task

The board comes from "fs status" only. cap writes nothing in fs and never
holds a claim. State is in memstore scopes cap:<project>; the trace is a file
at $CAP_TRACE (default ~/.cap/trace.log). The store is $MEMSTORE_DB, else
~/.memstore/memory.db. Set $CAP_FS_BIN to name the fs binary.
`)
}
