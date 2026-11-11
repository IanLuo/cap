# AGENTS.md — cap

## Intent

- cap watches the fs board on a 30 s clock and reports what changed plus a ranked recommendation. A separate on-demand instruction launches exactly one command for the task the user names.
- The locked contract is `.agents/specs/spec.md`; its lock marker is line 1. It is frozen input: do not re-litigate its decisions.
- cap is not a worker, not a dispatcher implementation, not an fs writer, and not autonomous. It reads the board, recommends, and runs one named instruction.

## Build decision

- **Go 1.27 + a nix flake**, matching fs and memstore. The flake builds one static binary with `buildGoModule`, committed `vendor/`, and `vendorHash = null`.
- Go because cap already shares memstore's SQLite file; importing the same memstore library keeps one store, one schema, and a fully offline test run. Shelling out to the `memstore` CLI would make `nix flake check` depend on an external binary.
- The binary is `cap`; the flake pins nixpkgs unstable and Go 1.27, copied from fs's environment.

## How to run / build / test

```bash
# Build, vet, format check, tests (the fs task's first declared check)
nix develop --command sh -c 'test -z "$(gofmt -l cmd internal)" && go build ./... && go vet ./... && go test ./... -count=1'

# Hermetic binary
nix build .
./result/bin/cap tick
./result/bin/cap watch          # 30 s clock, no model, no launcher
./result/bin/cap act <task-id>  # one instruction
./result/bin/cap status         # running, pid, last tick, last error
./result/bin/cap start          # start cap's launchd label
./result/bin/cap stop           # stop cap's launchd label
./result/bin/cap logs --tail 20 # the operator view of the trace
```

- Tests use a temporary memstore file plus fake board and launcher. They need no herdr, no network, and never touch the real board.
- `MEMSTORE_DB` names the store (default `~/.memstore/memory.db`). `CAP_FS_BIN` names the fs binary (default `fs`). `CAP_TRACE` names the trace file (default `~/.cap/trace.log`).
- `nix develop` is required: `go` is not on the host PATH.

## Hot invariants

- **The board is `fs status` only.** `internal/state.Recent` refuses any scope not prefixed `cap:`, and `SearchKnowledge` returns only kind `knowledge`. Never read `fs.*` rows through memstore.
- **The tick has no launcher.** `Watcher` has no launcher field; only `Dispatcher.Act` can start a command. Do not add one to the clock path.
- **cap writes nothing in fs and never claims.** The code has no `fs add/claim/report/done`; its board seam has one read method.
- **cap owns three kinds:** `cap.act`, `cap.question`, `cap.cursor`, in `cap:<project>`; the board-wide cursor is `cap:cap`. One open act per task, one open question per task and gap, one open cursor. The cursor is written only when the board changes.
- **State rows are immutable.** An update is close the old row and write a new one; history stays closed and inert. Never rewrite a record in place.
- **The 90 s gate uses the newest act's `launched_at`, including closed acts.** Inside the gate: not recommended, not launched, and the refusal names the earlier launch.
- **The act is recorded before the command starts; a failed start closes it.** A crash between the two leaves an open act that still holds the gate, never a launch nothing knows about.
- **Fail closed.** Unreadable board, store, or command means non-zero exit, no open act record, no launch. No fallback store, no retry.
- **Rank:** unheld review before unheld dev; then fs's oldest-first task order (preserved as the board returns it). A task with an unconfirmed act younger than 90 s is skipped. A review whose newest review report is `fail` or `blocked` is skipped until its parent dev task reports a `dev-done` newer than that verdict.
- **Knowledge is never edited by cap.** A dispatch record must name exactly one `cap.dispatch:` command. Zero candidates or two distinct commands raises exactly one question and launches nothing.
- **The trace is a file, not a store.** One JSON line per tick and per act, rotated at 1 MiB. `cap status` and `cap logs` read it for the operator; no decision does, and it never gates a decision.
- **The interface controls one label.** `status`, `start`, `stop` and `logs` act on `home-manager.cap` only. They never launch a task command, never read fs, and never touch the store. `start` leaves a running watcher running; `stop` bootouts the label.
- **One watcher.** The nix module (`homeManagerModules.default`) runs `cap watch` under launchd with KeepAlive. Do not add a second supervisor or a second label.

## Knowledge record format

A kind `knowledge` memory with marker lines. `cap.task-kind` and `cap.project` are optional; absent means every kind or every project. `cap.project` takes one or more comma-separated project names.

```text
cap.dispatch: fs dispatch --project {project} --task {task} --dir {path}
cap.agent: fs
cap.task-kind: dev
cap.project: cap
```

Placeholders: `{task}`, `{project}`, `{path}`, `{kind}`. `cap.agent` names the agent kind recorded on `cap.act`; absent records `unspecified`. A command from knowledge may start an agent through fs; the `fs.launch` row is fs's own record, not a write by cap.

## Architecture elevator

```text
cmd/cap/          CLI: tick, watch, act, status, start, stop, logs; env wiring; exit codes
internal/board/   one fs status read, JSON envelope, Task type
internal/state/   the three cap record kinds; the only memstore importer
internal/watcher/ tick (no launcher), act (one launcher), rank, knowledge, report
internal/launch/  detached sh -c in the project directory; knows nothing about herdr
internal/service/ one launchd label: status, start, stop; knows nothing about tasks
internal/trace/   one JSON line per tick and act; rotates; never gates
vendor/           memstore + pure-Go sqlite, committed for hermetic builds
```

## Deeper docs

- `.agents/specs/spec.md` — locked requirements, AC1–AC13, NFRs, verification table.
