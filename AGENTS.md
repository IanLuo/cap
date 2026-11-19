# AGENTS.md — cap

## Intent

- cap watches the fs board and the agent harness on a 30 s clock. Ticks report what changed plus a ranked recommendation, and take tier-1 cleanup: reap a closed round's fs panes, close empty orphans, nudge a stalled agent, announce a blocked one. A separate on-demand instruction launches exactly one command for the task the user names.
- The locked contract is `.agents/specs/spec.md`; its lock marker is line 1. It is frozen input: do not re-litigate its decisions.
- cap is not a worker, not a dispatcher implementation, not an fs writer, and not autonomous over work. It reads the board and harness, recommends, cleans up, and runs one named instruction.

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
./result/bin/cap act <task-id>  # one instruction (needs the claim)
./result/bin/cap claim          # take or renew the claim on the write verbs (--pane names your pane)
./result/bin/cap status         # running, pid, last tick, last error, claim
./result/bin/cap start          # start cap's launchd label (needs the claim)
./result/bin/cap stop           # stop cap's launchd label (needs the claim)
./result/bin/cap logs --tail 20 # the operator view of the trace
```

- Tests use a temporary memstore file plus fake board, fake harness and fake reaper. They need no herdr, no network, and never touch the real board.
- `MEMSTORE_DB` names the store (default `~/.memstore/memory.db`). `CAP_FS_BIN` names the fs binary (default `fs`). `CAP_HERDR_BIN` names the harness binary (default `herdr`). `CAP_TRACE` names the trace file (default `~/.cap/trace.log`). `CAP_SESSION` is the caller's identity for the claim; `--session` overrides it. `HERDR_PANE_ID`, set by herdr in every pane, is the caller's address; `cap claim` records it unless `--pane` names another.
- `nix develop` is required: `go` is not on the host PATH.

## Hot invariants

- **The board is `fs status` only; the agent view is `herdr pane list` only.** `internal/state.Recent` refuses any scope not prefixed `cap:`, and `SearchKnowledge` returns only kind `knowledge`. Never read `fs.*` rows through memstore, and never read pane text, a transcript, or a diff.
- **The tick has no launcher.** `Watcher` has no launcher field; only `Dispatcher.Act` can start a command. Tier-1 actions are cleanup through the harness seam and `fs reap`, never a dispatch. Do not add a launcher to the clock path.
- **cap writes nothing in fs and never claims there.** The code has no `fs add/claim/report/done`; its board seam has one read method plus `Reap`. The only claim cap holds is `cap.claim`, its own tool's write lease.
- **cap owns five kinds:** `cap.act`, `cap.action`, `cap.claim`, `cap.question`, `cap.cursor`, in `cap:<project>`; the board-wide cursor, claim and orphan scope is `cap:cap`. One open act per task, one open question per task and gap, one open cursor, one open claim. The cursor is written only when the board or the harness changes.
- **One claimant holds the write verbs.** `act`, `start` and `stop` run only for the session that holds a live `cap.claim` (120 s, renewed by the holder's write); any other session is refused with the holder and its expiry named. An unclaimed or stale cap is claimed only with `--confirm`. `tick`, `watch`, `logs` and `status` need no claim. The claim also records the holder's pane (`cap claim --pane`), which is the tick's one address for it; the pane is self-declared, like the session id. The claim coordinates; a session id is self-declared, so it is not authentication.
- **Tier 1 acts only on a transition** between the previous cursor and this read, never on elapsed time alone. An empty pane's grace delays a transition already seen; it never starts one. With no previous cursor, or a cursor that kept no pane snapshot, the tick is the baseline: it calls `fs reap` once per project that still has an open fs pane, and no later tick reaps without a board transition. A round fs kept alive because its agent was working is reaped again on the harness transition from `working` to any other status, once the launch row's round is over; cap never closes that pane itself.
- **One action per task and class per 90 s, recorded before the side effect.** The gate and the refusal name the earlier action, exactly as `cap act` does. A failed action is recorded and reported, never retried inside the gate.
- **An fs-launched pane is reaped by fs, never closed by cap.** A pane that never held an agent is a human terminal: never closed. A pane that held an agent and lost it, with no launch row, is closed only after the 300 s grace. A pane that still holds an agent and has no launch row is human-owned: cap announces a blocked one and never closes it.
- **A blocked agent is never answered.** A transition into `blocked` writes one question and posts one notification; cap sends no prompt and no keys.
- **The tick tells the claim holder what it did.** After its tier-1 actions, one prompt goes to the live claim's pane: one line per action, naming the class, project and task goal. It is news, never an instruction; it is skipped when no claim is live or when the holder's pane is unlisted or `blocked`.
- **A closed round is announced.** When a dev task leaves the board, cap posts one notification, `cap: round closed`, with the body `<project> <task> done, review passed`. The review task that closed the same round posts none; cap answers nothing.
- **Nudge is bounded.** At most two prompts per task per fs round, and only to an idle agent on a claimed task whose lease has lapsed. A working agent is never prompted. A task cap nudged or announced in a tick is not reaped in that tick: fs closes an idle agent whose lease lapsed, so the reap would undo the prompt before the agent could renew.
- **State rows are immutable.** An update is close the old row and write a new one; history stays closed and inert. Never rewrite a record in place.
- **The 90 s gate uses the newest act's `launched_at`, including closed acts.** Inside the gate: not recommended, not launched, and the refusal names the earlier launch.
- **The act is recorded before the command starts; a failed start closes it.** A crash between the two leaves an open act that still holds the gate, never a launch nothing knows about.
- **Fail closed.** Unreadable board, store, or command means non-zero exit, no open act record, no launch. No fallback store, no retry. An unreadable harness skips tier 1 for that tick only: the cursor is not written, so the next readable tick replays the transition; it never stops the board read or the report.
- **Rank:** unheld review before unheld dev; then fs's oldest-first task order (preserved as the board returns it). A task with an unconfirmed act younger than 90 s is skipped. A review whose newest review report is `fail` or `blocked` is skipped until its parent dev task reports a `dev-done` newer than that verdict.
- **Knowledge is never edited by cap.** A dispatch record must name exactly one `cap.dispatch:` command. Zero candidates or two distinct commands raises exactly one question and launches nothing.
- **The trace is a file, not a store.** One JSON line per tick, per act and per tier-1 decision (`kind` `action`: class, target, project, task, pane, trigger, result), rotated at 1 MiB. `cap status` and `cap logs` read it for the operator; no decision does, and it never gates a decision.
- **The interface controls one label.** `status`, `start`, `stop` and `logs` act on `home-manager.cap` only. They never launch a task command and never read fs. `start` leaves a running watcher running; `stop` bootouts the label. `start` and `stop` read and write the one `cap.claim` row, and `status` reads it; `logs` touches no store.
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
cmd/cap/          CLI: tick, watch, act, claim, status, start, stop, logs; env wiring; exit codes
internal/board/   one fs status read and one fs reap, JSON envelope, Task type
internal/agents/  one herdr pane list and the three bounded actions: prompt, close, notify
internal/state/   the five cap record kinds; the only memstore importer
internal/watcher/ tick (no launcher, tier 1), act (one launcher), rank, knowledge, report
internal/launch/  detached sh -c in the project directory; knows nothing about herdr
internal/service/ one launchd label: status, start, stop; knows nothing about tasks
internal/trace/   one JSON line per tick, act and tier-1 decision; rotates; never gates
vendor/           memstore + pure-Go sqlite, committed for hermetic builds
```

## Deeper docs

- `.agents/specs/spec.md` — locked requirements, AC1–AC18, NFRs, verification table.
