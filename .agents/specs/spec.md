<!-- specs:locked:ce86436 2026-11-18 type=spec -->

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

- **Every act cap takes ends in a recorded verdict that a stranger can read from the store. Work starts only on the user's instruction; cleanup acts on an observed state transition.**
- Falsifiable, both halves: a tick starts no work and takes at most one cleanup action per task and class, recorded before it runs; an instruction on one task launches one command and leaves one `cap.act` record naming the command, the knowledge record used, the time and the observed result.

## Scope in / out + primary flow

**In**

- **A watcher.** A deterministic process on a 30 s clock. One `fs status`, a comparison against the last known board, and a report of what changed plus the task(s) cap would work on next.
- **A recommendation.** Task, project, kind, why it holds its rank, the command that would run, and any earlier failure. One line each.
- **An instruction.** The user names a task. cap resolves the command from knowledge, launches it detached, returns, and records one `cap.act`.
- **A confirmation.** A later tick that sees a holder updates the record. A dispatch that did not take is reported, not retried.
- **An ask.** Knowledge missing or ambiguous produces one `cap.question` and no command. cap never guesses.
- **A cleanup loop (tier 1).** On each tick, after the board read and the harness read: call `fs reap` when a round's fs-launched pane is still open; close a pane whose agent ended and no launch row owns, after a 300 s grace; nudge a claimed task whose lease lapsed while its process lives, at most twice per round; record a blocked agent's question and post one notification; and tell the live claim holder what it did, in one prompt, naming each action by class, project and task goal. The tick never dispatches.
- **An agent view.** One harness read per tick: pane id, agent status (`working`, `idle`, `blocked`, `done`, `gone`, `unknown`), and whether fs holds a launch row. cap never reads a pane's text.
- **A supervisor and an operator interface.** `cap watch` runs under launchd with KeepAlive. `cap status`, `cap start`, `cap stop` and `cap logs` let an agent see and control the watcher, each with `--json`. `cap status` and `cap logs` may read the trace; no decision does. None of them launches a task command.
- **One claimant holds the write verbs.** `cap act`, `cap start` and `cap stop` run only for the session that holds the one open `cap.claim`. Any other session is refused, and the refusal names the holder. An unclaimed or stale cap is claimed only with an explicit `--confirm`. The 30 s clock, `tick`, `logs` and `status` are outside the claim. The claim also names the holder's pane, self-declared like the session id, so one tick has one address for the holder; the tick reads the claim only to resolve that address.
- **Two forms, one boundary.** The watcher is a standalone no-AI process. An on-demand coding-agent interface reads its report and discusses choices. The agent never runs on a clock; the watcher never calls a model.

**Out — and what keeps it out**

- **Not a worker.** No code, no check run, no judgement. *Kept out by:* no working directory, no credentials, no diff is ever read.
- **Not a dispatcher of work.** No task is dispatched and no agent is started without a named instruction; a command to start work still comes from knowledge. *Kept out by:* the tick path has no dispatch and no knowledge lookup for work. It reaps, closes, nudges, and records.
- **Not an fs writer.** No task, no report, no fs claim, no review task. *Kept out by:* fs refuses every task write without a held claim, and cap never claims there.
- **Not autonomous over work.** The tick acts only on a state transition between two ticks, one action per task and class per 90 s, each recorded before the side effect. *Kept out by:* class 1 always leaves; an agent pane with no fs launch row is never closed; a blocked agent is never answered.
- **Not a store.** No database, no queue, no copy of the evidence. *Kept out by:* five kinds of state record in memstore and a local trace log.
- **Not a supervisor of workers.** launchd supervises cap; cap supervises rounds. It reaps through fs, nudges a stalled agent at most twice per round, and keeps no worker alive. *Kept out by:* no retry, no transcript read, no fs task write.
- **Not a scheduler with priorities.** No backlog, no memory between ticks except the cursor. *Kept out by:* a fixed rank and one act per instruction.
- **Not an author.** No task cards, no specs, no tools, no knowledge edits. *Kept out by:* the ask.
- **Not a reviewer.** The user and the agent judge the board; fs's `done` gate judges the work.

**Primary flow**

1. Every 30 s: one `fs status` over every project, and one harness read of the panes.
2. Compare with `cap.cursor`. A task is *changed* when its hold state, its lease, or its newest report differs from the last snapshot; a pane is *changed* when its agent status or its launch-row presence differs. No change: nothing is written and nothing is reported.
3. Report the changes, then the task(s) cap would work on next, by rank.
4. Tier-1 actions, one per task and class, each recorded before it runs: reap a closed round's fs pane, close an orphaned agent's pane after the grace, nudge a stalled agent, record and announce a blocked one. Then one prompt to the live claim holder's pane, reporting the actions this tick took. No transition, no action.
5. The user names a task. cap searches memstore for kind `knowledge` describing how to dispatch that kind of work in that project.
6. A command is found: launch it detached in the task's project, write one `cap.act`, return. No command, or two candidates: write one `cap.question`, launch nothing.
7. The next tick reports whether a holder appeared and updates the record. No auto-retry.
8. cap holds nothing in fs: no task claim, no lease, no pane it owns. Its only claim is `cap.claim`, which names the session allowed to run its write verbs. It reads panes; it closes only an orphaned agent's empty pane, and only through its own action record.

## Acceptance criteria

- **AC1 · One read, no dispatch on a tick.** Given two projects with open tasks, when a tick runs, then exactly one `fs status` call was made, no command from knowledge was launched, and no fs task state was written. Tier-1 effects are governed by AC14–AC18.
- **AC2 · One record per event.** Given a changed board, when a tick runs, then at most one open `cap.cursor` exists, replaced only on change. Given one instruction, then exactly one `cap.act` exists for it.
- **AC3 · A stranger reconstructs the act.** Given the task id, a reader combines the fs row (holder, lease, last report) with the `cap.act` record (command, agent kind, knowledge id, time, observed result) and knows what happened. Neither source repeats the other.
- **AC4 · Confirmed means held.** Given a launch, when a later tick shows a holder, then the record is confirmed. When no holder appears, then the record stays unconfirmed and the report names it. cap never marks a task failed.
- **AC4a · A recipe is scoped to the projects it names.** Given a knowledge record with `cap.project:`, then it matches only tasks whose project is listed. Given no `cap.project:` marker, it matches every project. A task whose project no record names gets one `cap.question` and no command.
- **AC4b · A review inherits its parent's path.** Given a task with no `project_path` and a `parent_dev` in the same project, then cap launches it in the parent dev task's `project_path`. Given neither, then cap asks and launches nothing.
- **AC5 · Missing knowledge asks.** Given no knowledge record names a command for the act, when the user instructs the task, then one `cap.question` is written, no command is launched, fs is unchanged, and a second instruction for the same gap opens no second question.
- **AC6 · cap writes nothing in fs.** Given any tick or act, then fs holds no new task, report, claim or review task from cap, and cap never appears as a holder. A command cap runs may start an agent through fs; the `fs.launch` row it leaves is fs's own record of a process fs started, not a write by cap. The smallest refusal: any attempt by cap to write there.
- **AC7 · The rank holds.** Given one unheld review and one unheld dev task, the recommendation names the review first. Given dev tasks only, the oldest. A task with an unconfirmed act younger than 90 s is not recommended again. A review whose newest review report is `fail` or `blocked` is not recommended while its parent dev task has no `dev-done` newer than that verdict; with a newer `dev-done`, the review is recommended again. Given no `parent_dev`, the review is recommended.
- **AC8 · The instruction gate.** Given an instruction for a task launched less than 90 s ago, then nothing is launched, and the refusal names the earlier launch.
- **AC9 · Zero tokens on the clock.** Given a day of ticks, then no model call was made by the watcher, and the report is text.
- **AC10 · Fail closed.** Given an unreadable board or store, then the tick exits non-zero, names the cause, and launches nothing.
- **AC11 · The watcher is supervised.** Given the module is enabled, when `cap watch` exits, then launchd restarts it, and only one watcher runs.
- **AC12 · The interface controls only the watcher.** Given `cap status`, `cap start`, `cap stop`, or `cap logs`, then no task command runs, fs is unchanged, no knowledge is edited, and start and stop act on cap's own label alone.
- **AC13 · Every verb is machine-readable.** Given `--json`, then the verb prints JSON on stdout — one object per call, one per tick for `watch` — and exits 0 on success, 1 on failure, 2 on usage.
- **AC14 · Tier 1 acts only on transitions.** Given a round that just closed and a pane fs launched that is still open, one tick calls `fs reap` once and never closes that pane. Given a tick with no previous cursor, or a cursor that kept no pane snapshot, then that tick is the baseline: it calls `fs reap` once for each project that has an fs launch row whose pane is still open in the harness. A baseline is a first observation, not a timer; no later tick reaps without a board transition. Given a pane fs launched whose round is over — its task is closed, or its review's newest word is `fail` or `blocked` — and fs kept that pane at the round's close because its agent was still working, then the harness transition from `working` to any other status is the trigger: one tick calls `fs reap` once for the project, and cap still never closes the pane. Given no transition, the tick acts not at all.
- **AC15 · One action per task and class per 90 s.** Given a repeated transition inside the gate, no second action runs, and the refusal names the earlier action.
- **AC16 · Nudge is bounded.** Given a claimed task with a lapsed lease and a live process, cap prompts the agent with the goal and declared checks, at most twice per round; a working agent is never prompted.
- **AC17 · An orphan closes only when empty.** Given a pane that held an agent and now has none, with no launch row, cap closes it after the grace period. Given a pane that never held an agent, or a pane that still holds one with no launch row, cap never closes it.
- **AC18 · Blocked is recorded and announced, never answered.** Given a transition into `blocked`, cap writes one question and posts one notification, sends no prompt and no keys, and writes nothing further while the state does not change.
- **AC19 · One claimant holds cap.** Given `cap act`, `cap start` or `cap stop`, then it runs only when the caller's session holds an open `cap.claim` whose 120 s lease is live; otherwise it exits non-zero, launches no command, touches no label, and the refusal names the holder and its expiry. Given no open claim, or a claim whose lease has lapsed, then `cap claim --session S [--pane P] --confirm` writes one claim whose records the pane when one is given; without `--confirm` nothing is written and the refusal names the state and any earlier holder. Given a live claim by another session, then no claim is written. The holder's `claim`, `act`, `start` and `stop` renew the lease. Given `cap status`, then it names the live holder, its pane and its expiry or, when no claim is live, the last one; it never takes the claim. `tick`, `watch` and `logs` never need a claim.
- **AC20 · Tier-1 decisions are in the trace.** Given any tier-1 decision, then the trace holds one line with `kind` `action`, its `class` (`reap`, `close`, `nudge`, `blocked` or `inform`), the target, project, task and pane it names, the trigger, and the result — `done`, `failed` with the error, or `refused` by the gate. No decision reads the trace, and a tick with no tier-1 decision writes no action line.
- **AC21 · A closed round is announced.** Given a dev task leaves the board because its round completed, then cap posts one notification, title `cap: round closed`, body `<project> <task> done, review passed`. It fires once, on the transition, and never replaces the reap. A review task leaving the board posts none, and a tick in which no dev task leaves the board posts none.
- **AC22 · The tick informs the claim holder.** Given a tick that takes one or more tier-1 actions and a live `cap.claim` that records a pane, then cap sends one prompt to that pane: one line per action this tick took, each naming its class, its project and its task by goal, and the outcome. It sends none when no claim is live, when the claim records no pane, when the harness does not list that pane, or when that pane's agent status is `blocked`. It prompts no other pane on this account and never a working agent's task on purpose. The prompt reports; it never instructs, answers a question, or changes what an action does. A failed prompt is recorded and not retried in that tick.

## KPIs & failure signal

- **Good:** every actable task appears in a recommendation within two ticks (≤60 s); at least 70% of recommendations are acted on; the age of the oldest actable task falls week over week.
- **Clean:** no fs-launched pane outlives its round by more than two ticks; no orphaned agent pane outlives the grace period; a blocked agent is announced within one tick.
- **Actable:** a task cap could act on now. A review whose newest review report is `fail` or `blocked`, with no newer `dev-done` on its parent dev task, is not actable.
- **Measured from what exists:** fs rows give actable-since and held-at; the watcher's trace log gives recommendations and instructions. No telemetry, no counters in the store.
- **Off-switch: fewer than 30% of recommendations are acted on for two weeks.** Then the rank is wrong or the board is not the bottleneck, and cap is adding attention rather than saving it. The remedy is the rank or the knowledge, never more recommendations.
- **Second signal:** an actable task older than a week that cap has recommended and the user rejected twice — the rank and the user disagree about what matters.

## Non-functional requirements

- **N1 · Clock, not a wait.** A tick every 30 s; a tick returns in milliseconds; an actable change is reported within 60 s.
- **N2 · The clock costs no tokens.** The watcher's path has no model client. Tokens exist only in the on-demand agent conversation, and cap's part of it is bounded: resolve a command, run it, record it.
- **N3 · One board read, no reading of work.** One `fs status` and one harness read per tick. cap reads pane and agent status; it never reads a pane's text, a diff, or a transcript.
- **N4 · Bounded state.** One open record per task and per gap; the cursor is written only when the board or the harness changes; an action row is written only when an action runs. Ten times worse would turn memstore into a tick log and degrade every other project that shares the file.
- **N5 · Fail closed.** Unreadable board, store, or command means no act, no act record, and a non-zero exit naming the cause. An unreadable harness skips tier 1 for that tick; it never stops the board read or the report.
- **N6 · One watcher at a time, by convention.** launchd holds one `cap watch`; a start on a loaded agent does not add a second. A second watcher is otherwise safe but redundant; no lock is kept, and fs's claim is what arbitrates a duplicated launch.

## Assumptions & dependencies

| # | assumption | falsified by | then |
|---|---|---|---|
| A1 | fs starts agents (`dispatch`, `reap`); how to start one is knowledge, and cap runs the named command through its generic detached launcher. | knowledge names no command, or names two. | cap asks; it never falls back to a hard-coded launcher. |
| A2 | how to start a worker is knowledge, not code. Today it is herdr. | knowledge names no command, or names two. | cap asks; it never falls back to a hard-coded launcher. |
| A3 | memstore is the only store; cap owns no database. | cap needs state memstore cannot hold. | record it as state and name what is lost; no second store. |
| A4 | a locked spec doc is the contract, found by grep. | a task has no locked doc. | nothing: the doc is the user's input, not cap's. cap reads no doc and refuses only on an unreadable board, store, or command, or on missing or ambiguous knowledge. |
| A5 | a worker's health is observable: from its fs reports, or from the harness (`working`, `idle`, `blocked`). | neither source is readable. | cap reports `unknown` and never treats silence as working; an unreadable harness skips tier 1 for that tick. |
| A6 | a worker is prompted once and works; fs's 120 s lease is the worker's clock, not cap's. A nudge repeats the goal and declared checks to the same agent, at most twice per round. | the worker blocks or asks. | the tick reports or notifies the state it sees; the user answers. cap never polls a worker. |
| A7 | a merge is a worker's job, not the coordinator's. | no worker is free to merge. | the user merges and says so; cap records only what it launched. |
| A8 | builds are network-free; the flake builds a static binary. | a declared check needs the network. | the check fails and is reported once. |

## Data requirements

- **Inputs:** one `fs status` over every project; one harness read of the panes (id, agent status, launch-row presence); memstore rows filtered to kind `knowledge`; the task's `project_path`, or its parent dev task's when absent, for the launcher's working directory.
- **cap's state, kinds it owns, scope `cap:<project>`:**
  - `cap.act` — one open record per task: command, agent kind, knowledge id, launched-at, observed result.
  - `cap.action` — one record per tier-1 act: class (`reap`, `close`, `nudge`), target (project, task, or pane), the observed trigger, time, outcome. Written before the side effect; a failed action closes with the error.
  - `cap.question` — one open record per gap: what is missing, which task needs it, when. A blocked agent is a gap of kind `blocked`.
  - `cap.cursor` — one open record: the last board snapshot (per open task: holder state, lease, newest report time) and the last harness snapshot (per pane: agent status, launch-row presence). Written only when the board or the harness changes.
  - `cap.claim` — one open record in `cap:cap`: the session that holds cap's write verbs, its pane when the holder named one, and when its 120 s lease ends. Closed and rewritten on each holder write and on a confirmed takeover. The write verbs, `cap status` and the tick's inform read it; no other tick decision does.
- **The board has one source.** cap never reads `fs.*` rows through memstore; those rows are fs's own, and reading them beside knowledge is how a supervisor confuses a task with a fact.
- **The trace is a file, not a store.** One JSON line per tick, per act and per tier-1 decision (`kind` `action`: class, target, project, task, pane, trigger, result) in cap's own log, rotated and deleted when large. It never gates a decision and cap never reads it.
- **Must survive:** the open `cap.act`, `cap.question` and `cap.claim` records and the newest `cap.action` per task and class, so a restart does not repeat an act, a question or an action, and a restart does not free the claim of a session that still works. Nothing else.
- **Empty and edge:** a tick with no change writes nothing. A board with no actable task reports nothing. A task with no `project_path` uses its `parent_dev` task's path; with neither, it cannot be dispatched: cap writes one question naming it. A question closes when a knowledge record answers it. A pane that held an agent and lost it, with no launch row, closes after the grace; a pane that never held an agent, and a pane that still holds one without a launch row, is never closed. A blocked agent gets one question and one notification, and is never answered.

## Rollback & recovery

- **Revert is cheap by construction:** cap writes nothing in fs, so stopping it leaves the board exactly as it was; panes it would have closed stay open. Its records become inert history that nothing reads.
- **"Reverted" means:** no watcher runs, no command launches, no pane closes, no question is raised, and no record is consulted. No migration, no cleanup.
- **The failure that would wreck it:** a broken command launched on every instruction, or a cleanup rule that closes a live pane. Each attempt leaves a pane and an agent that may burn tokens; a bad close loses work in progress. The instruction gate, the absence of auto-retry, the question, the transition-only trigger, class 1, and the 90 s action gate are what bound them.
- **After a crash:** the watcher re-reads its open records. An act launched less than 90 s ago is not repeated; an action taken less than 90 s ago is not repeated; the gates survive because they are state, not memory.
- **An orphaned pane** — an agent that ended without fs closing its pane — is closed only when it is empty and no launch row owns it, after the grace. A pane that never held an agent is the user's terminal, not an orphan: cap never closes it. An agent pane with no launch row stays visible to a human and is never closed by cap.
- **A bad knowledge record** is the other way this goes wrong. Its id is in every `cap.act` it caused, so the cause is one read away. The user corrects the record; cap never edits knowledge.
- **Fail closed:** an unreadable board or a refused store write ends the tick with an error and no act.

## Security & compliance

- **Secrets:** a record names an environment variable, never its value. No log line carries a token.
- **Destructive acts:** bounded. cap closes an orphaned agent's pane and calls `fs reap`; it posts a notification. It never closes a pane that never held an agent, never closes an agent pane without fs's launch row, never answers a blocked agent, never writes fs task state, merges nothing, confirms nothing. Its effects are one command run, one cleanup action, and one state record.
- **Provenance:** the knowledge id is a field on the `cap.act` record. Nobody walks a link graph; the trace of what ran lives in cap's log.
- **The worker route:** a guard, by claim. Any agent may read `status`, `logs` and `tick`. Only the session holding the live `cap.claim` may run `act`, `start` or `stop`, and a takeover of an unclaimed or stale claim needs `--confirm`. The claim coordinates, it does not authenticate: a session id is self-declared, so the guard stops accidental parallel use, not a caller that lies.
- **Regulatory:** N/A — one user, one machine, local files, no telemetry, no network egress, no personal data.

## Verification plan

| requirement | proven by |
|---|---|
| AC1 one read, no dispatch on a tick | fake launcher and temp store: count `fs status` calls; no `fs dispatch`; no fs task write. |
| AC2 one record per event | row counts after a changed tick and after one instruction. |
| AC3 a stranger reconstructs | a fresh session reads one `cap.act` and the fs row and names command, knowledge id, outcome. |
| AC4 confirmed means held | fake launcher that claims late, then never: confirmed on a later tick, then reported unconfirmed. |
| AC4a recipe is project-scoped | two records, one scoped to a and one unscoped: a task in b matches only the unscoped one; a task in c with only the scoped record asks. |
| AC4b review inherits its path | a review with no path and a parent dev: launched in the parent's path; with neither: one question, no command. |
| AC5 missing knowledge asks | no knowledge: one question, no command, fs unchanged, no second question. |
| AC6 cap writes nothing in fs | `fs status` before and after: identical; no claim row from cap. |
| AC7 the rank holds | one review and one dev unheld: review first; dev only: oldest; unconfirmed act inside the gate: not recommended; a failed review with no newer parent `dev-done`: not recommended and the parent dev is; after a newer `dev-done`: the review is first. |
| AC8 the instruction gate | two instructions inside 90 s: one launch, the second refused with the earlier time. |
| AC9 zero tokens | the watcher path has no model client; a test asserts no call over a simulated day. |
| AC10 fail closed | an unreadable store: non-zero exit, no launch, no record. |
| AC11 watcher supervised | nix eval of the module: one agent, KeepAlive, ProgramArguments `cap watch`; fake launchctl in the CLI tests. |
| AC12 interface touches nothing | fake fs and a temp store: every verb run, no task command, no fs argv, no knowledge write. |
| AC13 JSON verbs | one JSON object per verb with `--json`; exit 0/1/2. |
| AC14 tier 1 acts only on transitions | fake harness: one closed round with an open fs pane yields one `fs reap`; an unchanged tick yields none; cap never closes that pane. A store with no cursor: one reap per project with an open launch-row pane, and none on the next tick. A finished round whose pane was kept working, then stops working: the harness transition yields one reap, and none while the round is still open. |
| AC15 one action per task and class per 90 s | two transitions inside the gate: one action; the second refused and naming the first. |
| AC16 nudge is bounded | a claimed task with a lapsed lease and a live process: at most two prompts per round; a working agent gets none. |
| AC17 an orphan closes only when empty | an orphaned agent pane with no launch row closes after the grace; a pane that never held an agent, and an agent pane with no launch row, never close. |
| AC18 blocked is recorded and announced | one question and one notification on the transition; no prompt, no keys, and nothing new while the state holds. |
| AC19 one claimant holds cap | temp store: `act`, `start` and `stop` with no session, no claim, a foreign live claim, and a stale claim are each refused and name the holder; `claim` without `--confirm` writes nothing while unclaimed or stale, with it writes one, and a `--pane` is recorded and shown by `status`; a holder write renews the lease, and a second live session cannot take it. |
| AC20 tier-1 decisions are traced | a fake trace: a reap, a close, a nudge and a blocked transition each write one `action` line with class, target, task, pane, trigger and result; a gated repeat writes a `refused` line; an unchanged tick writes none. |
| AC21 a closed round is announced | fake harness: a dev task leaving the board posts one `cap: round closed` line naming the project and the task; a review task leaving the board posts none; a tick with no closed round posts none. |
| AC22 the tick informs the claim holder | fake harness and temp store: a tick with one action and a live claim with a listed pane prompts that pane once, with one line per action naming class, project and goal; no claim, a stale claim, an unlisted pane, a `blocked` pane and a tick with no action each send no prompt; a failed prompt is traced `failed` and not retried in the tick. |
| N6 one watcher | fake launchctl: start on a loaded agent issues kickstart, not bootstrap; stop issues one bootout. |
| N1 clock | time a tick against a slow board fixture. |
| N3 no reading of work | the fake harness records every call; only list, prompt, close and notify — no `agent read` and no `pane read`. |
| N4 bounded state | row count after ten unchanged ticks stays flat. |
| KPI precision | the trace log over two weeks answers the acted-on ratio. |
| independent check | one review task, in a session that did not write cap, reads this locked doc against the change and asks: can anything start work without an instruction, and can cap clean up without writing fs task state. |
| manual, once | one tick on the real board with a real unheld task, observed in fs and in the pane list. |

Last reviewed: 2026-11-18 · cap
