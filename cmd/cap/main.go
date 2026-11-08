// Command cap is the coordinator's watcher and advisor. `cap tick` reads the
// board and never launches. `cap act <task>` runs one instruction. `cap watch`
// repeats the tick on the 30 s clock. `cap status`, `start`, `stop` and `logs`
// are the operator interface around the supervised watcher: they control
// cap's own launchd label and never launch a task command.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/IanLuo/cap/internal/board"
	"github.com/IanLuo/cap/internal/launch"
	"github.com/IanLuo/cap/internal/service"
	"github.com/IanLuo/cap/internal/state"
	"github.com/IanLuo/cap/internal/trace"
	"github.com/IanLuo/cap/internal/watcher"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// options are the flags every verb accepts.
type options struct {
	json bool
	tail int
	args []string
}

func parseOpts(args []string) (options, error) {
	o := options{tail: 50}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--json":
			o.json = true
		case a == "--tail":
			if i+1 >= len(args) {
				return o, fmt.Errorf("--tail needs a number")
			}
			i++
			n, err := parseTail(args[i])
			if err != nil {
				return o, err
			}
			o.tail = n
		case strings.HasPrefix(a, "--tail="):
			n, err := parseTail(strings.TrimPrefix(a, "--tail="))
			if err != nil {
				return o, err
			}
			o.tail = n
		case strings.HasPrefix(a, "-"):
			return o, fmt.Errorf("unknown option %q", a)
		default:
			o.args = append(o.args, a)
		}
	}
	return o, nil
}

func parseTail(v string) (int, error) {
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("--tail wants a positive number, got %q", v)
	}
	return n, nil
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	verb := args[0]
	opts, err := parseOpts(args[1:])
	if err != nil {
		fmt.Fprintf(stderr, "cap: %v\n", err)
		usage(stderr)
		return 2
	}
	switch verb {
	case "tick":
		if len(opts.args) != 0 {
			usage(stderr)
			return 2
		}
		return tickCmd(opts, stdout, stderr)
	case "watch":
		if len(opts.args) != 0 {
			usage(stderr)
			return 2
		}
		return watchCmd(opts, stdout, stderr)
	case "act":
		if len(opts.args) != 1 {
			usage(stderr)
			return 2
		}
		return actCmd(opts.args[0], opts, stdout, stderr)
	case "status":
		if len(opts.args) != 0 {
			usage(stderr)
			return 2
		}
		return statusCmd(opts, stdout, stderr)
	case "start":
		if len(opts.args) != 0 {
			usage(stderr)
			return 2
		}
		return startCmd(opts, stdout, stderr)
	case "stop":
		if len(opts.args) != 0 {
			usage(stderr)
			return 2
		}
		return stopCmd(opts, stdout, stderr)
	case "logs":
		if len(opts.args) != 0 {
			usage(stderr)
			return 2
		}
		return logsCmd(opts, stdout, stderr)
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	default:
		usage(stderr)
		return 2
	}
}

func tickCmd(o options, stdout, stderr io.Writer) int {
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
	return writeReport(stdout, stderr, report, o.json)
}

func watchCmd(o options, stdout, stderr io.Writer) int {
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
		} else if code := writeReport(stdout, stderr, report, o.json); code != 0 {
			return code
		}
		<-ticker.C
	}
}

func actCmd(tid string, o options, stdout, stderr io.Writer) int {
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
	if o.json {
		return emitJSON(stdout, stderr, actJSON{
			Task: result.Task, Command: result.Command, Knowledge: result.Knowledge,
			Agent: result.Agent, Launched: result.Launched,
		})
	}
	fmt.Fprintf(stdout, "launched %s for %s (knowledge %s, agent %s)\n", watcher.ShortCommand(result.Command), result.Task, result.Knowledge, result.Agent)
	return 0
}

func statusCmd(o options, stdout, stderr io.Writer) int {
	ctx := context.Background()
	svc := service.New()
	st, err := svc.State(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "cap: %v\n", err)
		return 1
	}
	events, err := trace.Tail(tracePath(), 200)
	if err != nil {
		fmt.Fprintf(stderr, "cap: read trace: %v\n", err)
		return 1
	}
	s := newStatus(svc, st, events, time.Now())
	if o.json {
		return emitJSON(stdout, stderr, s)
	}
	fmt.Fprint(stdout, s.Text())
	return 0
}

func startCmd(o options, stdout, stderr io.Writer) int {
	ctx := context.Background()
	svc := service.New()
	if err := svc.Start(ctx); err != nil {
		fmt.Fprintf(stderr, "cap: %v\n", err)
		return 1
	}
	st, err := svc.State(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "cap: %v\n", err)
		return 1
	}
	a := newAction("start", svc, st)
	if o.json {
		return emitJSON(stdout, stderr, a)
	}
	fmt.Fprint(stdout, a.Text())
	return 0
}

func stopCmd(o options, stdout, stderr io.Writer) int {
	ctx := context.Background()
	svc := service.New()
	if err := svc.Stop(ctx); err != nil {
		fmt.Fprintf(stderr, "cap: %v\n", err)
		return 1
	}
	st, err := svc.State(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "cap: %v\n", err)
		return 1
	}
	a := newAction("stop", svc, st)
	if o.json {
		return emitJSON(stdout, stderr, a)
	}
	fmt.Fprint(stdout, a.Text())
	return 0
}

func logsCmd(o options, stdout, stderr io.Writer) int {
	events, err := trace.Tail(tracePath(), o.tail)
	if err != nil {
		fmt.Fprintf(stderr, "cap: read trace: %v\n", err)
		return 1
	}
	if o.json {
		return emitJSON(stdout, stderr, logsJSON{Path: tracePath(), Lines: events})
	}
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			continue
		}
		fmt.Fprintln(stdout, string(line))
	}
	return 0
}

func writeReport(stdout, stderr io.Writer, report watcher.Report, asJSON bool) int {
	if !asJSON {
		fmt.Fprint(stdout, report.String())
		return 0
	}
	return emitJSON(stdout, stderr, newReport(report))
}

func emitJSON(stdout, stderr io.Writer, v any) int {
	if err := json.NewEncoder(stdout).Encode(v); err != nil {
		fmt.Fprintf(stderr, "cap: encode json: %v\n", err)
		return 1
	}
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
	fmt.Fprint(w, `usage: cap <command> [--json]

  cap tick            one board read: report changes and the ranked next tasks
  cap watch           repeat the tick every 30 s (supervised by launchd)
  cap act <task>      run one instruction for a named task
  cap status          watcher state: running, pid, last tick, last error
  cap start           start the watcher's launchd agent
  cap stop            stop the watcher's launchd agent
  cap logs [--tail N] print the last N trace events (default 50)

  --json              one JSON object per call, one per tick for watch

The board comes from "fs status" only. cap writes nothing in fs and never
holds a claim. State is in memstore scopes cap:<project>; the trace is a file
at $CAP_TRACE (default ~/.cap/trace.log). The store is $MEMSTORE_DB, else
~/.memstore/memory.db. Set $CAP_FS_BIN to name the fs binary. The service is
$CAP_SERVICE_LABEL (default home-manager.cap); $CAP_LAUNCHCTL names launchctl.
`)
}
