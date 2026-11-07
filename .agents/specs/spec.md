<!-- specs:locked:2748f7c 2026-11-07 type=spec -->

## Link contract
- **upstream** (this doc relies on): none
- **referrers** (must cite this when they change): .agents/specs/

# Spec — cap: the coordinator's watcher and advisor
Type: spec · Owner: cap · Lock state: the marker above, nowhere else

## Problem & who breaks

- **Who:** the user as coordinator of agent work, and every task on the fs board that nobody acts on.
- **Break — nothing watches the board.** fs records tasks, holders, leases and reports. Acting on them requires a person to look. Evidence at the time of writing: 27 tasks, 7 open, 0 claimed; one dev task at `dev-done` for 72 hours with a dead lease, and three unheld review tasks in one project.
- **Break — the coordinator's context is the runtime.** Every dispatch, every status check and every hand-off is typed in one session. Parallel work costs attention, not seconds.
- **Cost of doing nothing:** the number of agents that can run stays capped by one person's attention, and a task nobody looks at stays open indefinitely.

## Non-negotiable outcome

- **Every act cap takes ends in a recorded verdict that a stranger can read from the store, and every act starts only on the user's instruction.**
- Falsifiable, both halves: a tick reads the board and starts nothing; an instruction on one task launches one command and leaves one `cap.act` record naming the command, the knowledge record used, the time and the observed result.

## Scope in / out + primary flow

**In**

- **A watcher.** A deterministic process on a 30 s clock. One `fs status`, a comparison against the last known board, and a report of what changed plus the task(s) cap would work on next.
- **A recommendation.** Task, project, kind, why it holds its rank, the command that would run, and any earlier failure. One line each.
- **An instruction.** The user names a task. cap resolves the command from knowledge, launches it detached, returns, and records one `cap.act`.
- **A confirmation.** A later tick that sees a holder updates the record. A dispatch that did not take is reported, not retried.
- **An ask.** Knowledge missing or ambiguous produces one `cap.question` and no command. cap never guesses.
- **Two forms, one boundary.** The watcher is a standalone no-AI process. An on-demand coding-agent interface reads its report and discusses choices. The agent never runs on a clock; the watcher never calls a model.

**Out — and what keeps it out**

- **Not a worker.** No code, no check run, no judgement. *Kept out by:* no working directory, no credentials, no diff is ever read.
- **Not a dispatcher implementation.** cap runs a command; it does not speak herdr, manage panes, or retry a worker. *Kept out by:* its own code never mentions herdr; a command comes from knowledge.
- **Not an fs writer.** No task, no report, no claim, no review task. *Kept out by:* fs refuses every task write without a held claim, and cap never claims.
- **Not autonomous.** Nothing launches without a named instruction. *Kept out by:* the tick path has no command launcher.
- **Not a store.** No database, no queue, no copy of the evidence. *Kept out by:* three kinds of state record in memstore and a local trace log.
- **Not a scheduler with priorities.** No backlog, no memory between ticks except the cursor. *Kept out by:* a fixed rank and one act per instruction.
- **Not an author.** No task cards, no specs, no tools, no knowledge edits. *Kept out by:* the ask.
- **Not a reviewer.** The user and the agent judge the board; fs's `done` gate judges the work.

**Primary flow**

1. Every 30 s: one `fs status` over every project.
2. Compare with `cap.cursor`. A task is *changed* when its hold state, its lease, or its newest report differs from the last snapshot. No change: nothing is written and nothing is reported.
3. Report the changes, then the task(s) cap would work on next, by rank.
4. The user names a task. cap searches memstore for kind `knowledge` describing how to dispatch that kind of work.
5. A command is found: launch it detached in the task's project, write one `cap.act`, return. No command, or two candidates: write one `cap.question`, launch nothing.
6. The next tick reports whether a holder appeared and updates the record. No auto-retry.
7. cap holds nothing: no claim, no lease, no pane it reads.

## Acceptance criteria

- **AC1 · One read, no act on a tick.** Given two projects with open tasks, when a tick runs, then exactly one `fs status` call was made and no command was launched.
- **AC2 · One record per event.** Given a changed board, when a tick runs, then at most one open `cap.cursor` exists, replaced only on change. Given one instruction, then exactly one `cap.act` exists for it.
- **AC3 · A stranger reconstructs the act.** Given the task id, a reader combines the fs row (holder, lease, last report) with the `cap.act` record (command, agent kind, knowledge id, time, observed result) and knows what happened. Neither source repeats the other.
- **AC4 · Confirmed means held.** Given a launch, when a later tick shows a holder, then the record is confirmed. When no holder appears, then the record stays unconfirmed and the report names it. cap never marks a task failed.
- **AC5 · Missing knowledge asks.** Given no knowledge record names a command for the act, when the user instructs the task, then one `cap.question` is written, no command is launched, fs is unchanged, and a second instruction for the same gap opens no second question.
- **AC6 · cap writes nothing in fs.** Given any tick or act, then fs holds no new task, report, claim or review task from cap, and cap never appears as a holder. The smallest refusal: any attempt by cap to write there.
- **AC7 · The rank holds.** Given one unheld review and one unheld dev task, the recommendation names the review first. Given dev tasks only, the oldest. A task with an unconfirmed act younger than 90 s is not recommended again.
- **AC8 · The instruction gate.** Given an instruction for a task launched less than 90 s ago, then nothing is launched, and the refusal names the earlier launch.
- **AC9 · Zero tokens on the clock.** Given a day of ticks, then no model call was made by the watcher, and the report is text.
- **AC10 · Fail closed.** Given an unreadable board or store, then the tick exits non-zero, names the cause, and launches nothing.

## KPIs & failure signal

- **Good:** every actable task appears in a recommendation within two ticks (≤60 s); at least 70% of recommendations are acted on; the age of the oldest actable task falls week over week.
- **Measured from what exists:** fs rows give actable-since and held-at; the watcher's trace log gives recommendations and instructions. No telemetry, no counters in the store.
- **Off-switch: fewer than 30% of recommendations are acted on for two weeks.** Then the rank is wrong or the board is not the bottleneck, and cap is adding attention rather than saving it. The remedy is the rank or the knowledge, never more recommendations.
- **Second signal:** an actable task older than a week that cap has recommended and the user rejected twice — the rank and the user disagree about what matters.

## Non-functional requirements

- **N1 · Clock, not a wait.** A tick every 30 s; a tick returns in milliseconds; an actable change is reported within 60 s.
- **N2 · The clock costs no tokens.** The watcher's path has no model client. Tokens exist only in the on-demand agent conversation, and cap's part of it is bounded: resolve a command, run it, record it.
- **N3 · One board read, no reading of work.** One `fs status` per tick. cap never runs `agent read`, never reads a pane, a diff, or a transcript.
- **N4 · Bounded state.** One open record per task and per gap; the cursor is written only when the board changes. Ten times worse would turn memstore into a tick log and degrade every other project that shares the file.
- **N5 · Fail closed.** Unreadable board, store, or command means no act, no act record, and a non-zero exit naming the cause.
- **N6 · One watcher at a time, by convention.** A second watcher is safe but redundant; no lock is kept, and fs's claim is what arbitrates a duplicated launch.

## Assumptions & dependencies

| # | assumption | falsified by | then |
|---|---|---|---|
| A1 | fs keeps five verbs and never starts a process. | fs grows its own dispatch. | cap calls it and drops its own launcher. |
| A2 | how to start a worker is knowledge, not code. Today it is herdr. | knowledge names no command, or names two. | cap asks; it never falls back to a hard-coded launcher. |
| A3 | memstore is the only store; cap owns no database. | cap needs state memstore cannot hold. | record it as state and name what is lost; no second store. |
| A4 | a locked spec doc is the contract, found by grep. | a task has no locked doc. | cap refuses the task and names the missing doc. |
| A5 | a worker's health is observable: from its fs reports, or from the harness when knowledge says how. | neither source is readable. | cap reports `unknown` and never treats silence as working. |
| A6 | a worker is prompted once and works; fs's 120 s lease is the worker's clock, not cap's. | the worker blocks or asks. | the tick reports the state it sees; the user answers. cap never polls a worker. |
| A7 | a merge is a worker's job, not the coordinator's. | no worker is free to merge. | the user merges and says so; cap records only what it launched. |
| A8 | builds are network-free; the flake builds a static binary. | a declared check needs the network. | the check fails and is reported once. |

## Data requirements

- **Inputs:** one `fs status` over every project; memstore rows filtered to kind `knowledge`; the task's `project_path` for the launcher's working directory.
- **cap's state, kinds it owns, scope `cap:<project>`:**
  - `cap.act` — one open record per task: command, agent kind, knowledge id, launched-at, observed result.
  - `cap.question` — one open record per gap: what is missing, which task needs it, when.
  - `cap.cursor` — one open record: the last board snapshot (per open task: holder state, lease, newest report time). Written only when the board changes.
- **The board has one source.** cap never reads `fs.*` rows through memstore; those rows are fs's own, and reading them beside knowledge is how a supervisor confuses a task with a fact.
- **The trace is a file, not a store.** One JSON line per tick and per act in cap's own log, rotated and deleted when large. It never gates a decision and cap never reads it.
- **Must survive:** the open `cap.act` and `cap.question` records, so a restart does not repeat an act or a question. Nothing else.
- **Empty and edge:** a tick with no change writes nothing. A board with no actable task reports nothing. A task with no `project_path` cannot be dispatched: cap writes one question naming it. A question closes when a knowledge record answers it.

## Rollback & recovery

- **Revert is cheap by construction:** cap writes nothing in fs, so stopping it leaves the board exactly as it was. Its records become inert history that nothing reads.
- **"Reverted" means:** no watcher runs, no command launches, no question is raised, and no record is consulted. No migration, no cleanup.
- **The failure that would wreck it: a broken command launched on every instruction.** Each attempt leaves a pane and an agent that may burn tokens. The instruction gate, the absence of auto-retry, and the question are what bound it.
- **After a crash:** the watcher re-reads its open records. An act launched less than 90 s ago is not repeated; the gate survives because it is state, not memory.
- **An orphaned pane** — killed mid-launch, or an agent that never claimed — is not cleaned by cap; it deletes nothing. The report names the task, and the pane is visible to a human.
- **A bad knowledge record** is the other way this goes wrong. Its id is in every `cap.act` it caused, so the cause is one read away. The user corrects the record; cap never edits knowledge.
- **Fail closed:** an unreadable board or a refused store write ends the tick with an error and no act.

## Security & compliance

- **Secrets:** a record names an environment variable, never its value. No log line carries a token.
- **Destructive acts:** none. cap deletes nothing, writes nothing in fs, merges nothing, confirms nothing. Its effects are one command run and one state record.
- **Provenance:** the knowledge id is a field on the `cap.act` record. Nobody walks a link graph; the trace of what ran lives in cap's log.
- **The worker route:** no guard in V1, said plainly. A worker that runs cap only launches a command for a task that already exists, and fs's claim still arbitrates. If that proves wrong, the guard is a refusal when cap's own environment is a worker's.
- **Regulatory:** N/A — one user, one machine, local files, no telemetry, no network egress, no personal data.

## Verification plan

| requirement | proven by |
|---|---|
| AC1 one read, no act on a tick | fake launcher and temp store: count `fs status` calls and launches. |
| AC2 one record per event | row counts after a changed tick and after one instruction. |
| AC3 a stranger reconstructs | a fresh session reads one `cap.act` and the fs row and names command, knowledge id, outcome. |
| AC4 confirmed means held | fake launcher that claims late, then never: confirmed on a later tick, then reported unconfirmed. |
| AC5 missing knowledge asks | no knowledge: one question, no command, fs unchanged, no second question. |
| AC6 cap writes nothing in fs | `fs status` before and after: identical; no claim row from cap. |
| AC7 the rank holds | one review and one dev unheld: review first; dev only: oldest; unconfirmed act inside the gate: not recommended. |
| AC8 the instruction gate | two instructions inside 90 s: one launch, the second refused with the earlier time. |
| AC9 zero tokens | the watcher path has no model client; a test asserts no call over a simulated day. |
| AC10 fail closed | an unreadable store: non-zero exit, no launch, no record. |
| N1 clock | time a tick against a slow board fixture. |
| N3 no reading of work | the fake harness records every call; no `agent read` and no `pane read`. |
| N4 bounded state | row count after ten unchanged ticks stays flat. |
| KPI precision | the trace log over two weeks answers the acted-on ratio. |
| independent check | one review task, in a session that did not write cap, reads this locked doc against the change and asks: can cap act without writing in fs, and can anything launch without an instruction. |
| manual, once | one tick on the real board with a real unheld task, observed in fs and in the pane list. |

Last reviewed: 2026-11-07 · cap
