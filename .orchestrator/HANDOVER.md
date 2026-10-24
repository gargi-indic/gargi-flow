# Orchestrator Handover: gargi-flow

Snapshot refreshed on **2026-10-24 (~16:42 UTC)**, mid-**Epic #136 (Simulation P1)**: 9 of 11 top-level tasks closed (#137–#145), #146 at 1/3 with #204 in progress on Jules, then #205 and #206, and #147.
The **live** state is in issue **#67** (body tables + latest comments) and on `main`. #67 overrides this file wherever they differ.

---

## 1. What this project is
- **Engine (done):** **gargi-flow** is a Go middleware for reactive, durable DAG execution. It runs on Temporal, with pluggable stores (memory/Redis/Postgres/Neo4j), gRPC/REST/SSE/WebSocket APIs, and a React Flow canvas in `web/`. Spec: [`docs/SPEC.md`](../docs/SPEC.md).
- **Product being built now:** a **simulation and decision-optimization engine** on top of the same graph model. The idea is to model a decision process once, simulate it across uncertain worlds without Temporal, find the best policy, then deploy it on the engine. Architecture: [`docs/simulation/ARCHITECTURE.md`](../docs/simulation/ARCHITECTURE.md). Boundaries: [`docs/adr/0009-simulation-engine.md`](../docs/adr/0009-simulation-engine.md). Reference prototype for the golden numbers: [`docs/simulation/prototypes/loan_sim.py`](../docs/simulation/prototypes/loan_sim.py).
- **How it is built:** the orchestrator agent assigns sub-issues one at a time to **Jules** (Google coding agent, via `.orchestrator/jules.py`), or to a **Claude Code sub-agent** for `agent/claude` issues and Jules failures. It reviews plans and PRs, squash-merges into `main`, and logs every step in #67.

## 2. Where everything lives
| Thing | Location |
|---|---|
| Repo | `gargi-indic/gargi-flow` (default branch `main`) |
| Local clone | `/home/gishnum/code/gargi-ckg` (WSL `Ubuntu-24.04`; Go 1.26 installed; pinned lint at `bin/golangci-lint-v2.14.0`) |
| Live log + status tables | Issue **#67** |
| Jules helper | `.orchestrator/jules.py`; one-shot check-in: `.orchestrator/checkin.sh <session> <secs> <issue>` (REST state + Jules CLI + PRs + newest activities) |
| Resume skill | `.claude/skills/gargi-orchestrator/SKILL.md` |
| Credentials | `~/.config/jules/env` (`GH_TOKEN`, `JULES_API_KEY`). Run `source` on it in the same shell as `gh`/`jules.py`. **Never** print or commit it. |
| Jules CLI | `npx @google/jules` (v0.1.42, cached at `~/.npm/_npx/76db0a0f29ffdc9b/node_modules/.bin/jules`; not on PATH). `remote list --session` gives fresher status than the REST feed. `remote pull --session <id> [--apply]` gets the WIP patch for early review in a scratch worktree. It has no approve or message commands, so keep `jules.py` for those. |
| Claude sub-agent worktrees | `.claude/worktrees/agent-*` (harness-managed, untracked; do not commit) |

## 3. Backlog map
- All original backend epics (#1, #8, #15, #22, #29, #39, #48, #56, #61) and UI Epic #101 are complete and closed.
- **Simulation epics** (created 2026-10-22, label `area/simulation`):

| Epic | Scope | Status |
|---|---|---|
| #124 P0 | kernel + loan golden case | ✅ complete (#168–#179) |
| **#136 P1** | solvers, reports, API, CLI, MCP, canvas | **active**: 8/11 top-level tasks closed (see below) |
| #148 P2 | queues/resources, optimizers, agents | ready (needs-discussion: #152, #153, #158) |
| #159 P3 | deploy, outcomes, calibration, drift | ready |

### Epic #136 status (authoritative copy is the #67 body)
| Step | Issue | Status | Delivered by / PR |
|---|---|---|---|
| 1 | #137 backward-induction solver | ✅ | Opus · #180 |
| 2 | #138 params, grid/random search | ✅ | Jules → Opus · #181 |
| 3 | #139 metrics, regret, robust, Pareto | ✅ | Opus · #182 |
| 4 | #140 VOI, flip point, heatmap, policy tree | ✅ | Opus · #183 |
| 5 | #141 SimulationService API, job runner, memory store | ✅ | Opus (resumed from a dead agent's WIP) · #184 |
| 6 | #142 Postgres ExperimentStore | ✅ | Opus · #188 |
| 7 | #143 CLI server mode, reports, policy explain | ✅ closed | #185 Sonnet #189, #186 Sonnet #191, #187 Sonnet #192 |
| 8 | #144 MCP server (go-sdk v1.8.0) | ✅ closed | #193 **Jules** #200, #194 Jules FAILED → Opus #202, #195 Jules FAILED → Opus #207 |
| 9 | #145 canvas model editor, Decision node, panels | ✅ closed | #196 Sonnet #199, #197 **Jules** #201, #198 **Jules** #203 |
| 10 | #146 canvas Simulate + Results | 🛠 split | #204 in progress on Jules, then #205 (acceptance) → #206 |
| 11 | #147 canary rollout template, golden test, agent walkthrough | ⏳ | depends on #144 and #146; no spec yet |

**Epic #136 exit criterion:** an agent connected over MCP builds the canary-rollout case (case 3 in ARCHITECTURE.md) from a natural-language prompt, runs it, and returns a Pareto set of policies, all visible on the canvas.

## 4. Current state and immediate resume steps (2026-10-24, ~16:42 UTC)
0. **#195 merged via PR #207; #144 complete.** The Go MCP SDK v1.8.0 server is complete with model workspace repair loop, run/report/analysis tools, `model_this_decision` prompt, and acceptance tests. `main` is at `e628a59`.
1. **Active Jules session:** #204 (Simulate mode, #146 part 1/3) in session `2683794682960896782`.
   - Plan approved at 16:18 UTC.
   - Jules has created `web/e2e/fixtures/loan-report.json`, `web/src/sim/experiments.ts`, `web/src/components/SimulatePanel.tsx`, `web/src/components/SimModelEditor.tsx`, and `web/e2e/sim-simulate.spec.ts`.
   - **Resume:**
     - Run `.orchestrator/checkin.sh 2683794682960896782 1 204`.
     - When Jules opens a PR, review it locally. Require deletion of any `patch_*.py` / `test-results/` scratch files. Check Playwright test and web checks.
     - Squash-merge into `main`, tick #204's boxes, update #67, and start #205 on Jules.
2. **Queue after #204 (one Jules session at a time):**
   - #205 (Results overlay: policy tree per world, switcher, heatmap; acceptance test). Spec is on #205.
   - #206 (analysis panels, compare, replay). Needs its spec written first.
   - Then #147 (epic exit: canary-rollout template, golden test, agent walkthrough). Spec needed.
3. **No open PRs.** `main` is at `e628a59`.
4. **Open follow-ups**, logged in #67, none blocking:
   - Generate OpenAPI 3 types for `web/` so openapi-fetch types the sim routes; #196 used a cast.
   - Add `test-results/` and `playwright-report/` to `.gitignore`.
   - Pin the protoc plugin versions in the Makefile (the openapiv2 plugin must be v2.32.0).
   - Cache the policy list in `GetRun`.
   - `postgres.New` runs `CREATE SCHEMA` before the migration lock.

## 5. Decisions recorded (also in the #67 body)
- **Human (2026-10-22):**
  - Build the simulation engine as Epics #124/#136/#148/#159, P0 first.
  - **License deferred**; do not add a LICENSE file.
- **Human (2026-10-24): dispatch policy.** This replaces both the 2026-10-22 "Opus on Jules failure" rule and the 2026-10-23 "P1 Jules tasks straight to Opus" rule.
  - **Jules first.** Split large issues into small GitHub sub-issues (attach them with `gh api -X POST repos/gargi-indic/gargi-flow/issues/<parent>/sub_issues -F sub_issue_id=<id>`; `jules.py start` needs the parent link). Append an exact spec to each one.
  - **Adaptive check-ins:** 5 → 10 → 15 min, never longer.
  - **Fallback** when Jules misses **2 consecutive** check-ins or the session `FAILED`: a Claude sub-agent sized to the task (Haiku for mechanical work, Sonnet for moderate single-package work, Opus for spec-heavy or cross-cutting work).
  - **Parallel work:** a sub-agent may run in parallel with Jules only on disjoint files. #196 ran on Sonnet while Jules did #193, because the human asked for #145 to start immediately.
- **Human (2026-10-24): #144 uses the official Go MCP SDK** `github.com/modelcontextprotocol/go-sdk`, pinned at exactly **v1.8.0** (ADR-0010). Its transitive dependencies are approved; `golang.org/x/time` was bumped to v0.15.0 by MVS. No other new dependencies.
- **P0 defaults (orchestrator, open to override; ADR-0009 §7):**
  - The `decision` node is sim-registry only.
  - Abandonment ends the episode.
  - Constraints are per run.
  - No retries in simulation.
  - Policies cannot read `world`.
  - Sim error codes live in `pkg/sim/codes.go`.
- **Orchestrator defaults in P1** (recorded on each issue's "Implementation spec"):
  - **#141:** clock injected into `internal/sim/jobs`.
  - **#142:** Postgres stores BYTEA documents; `Runner.Recover` marks interrupted runs `failed`, assuming one server per database.
  - **#143:** `internal/simbackend` sits outside `internal/sim` and has Local and Remote backends.
  - **#194:** the MCP workspace is per server and holds YAML maps.
- **Open `needs-discussion`:** #152 (TPE in-house vs library), #153 (agent provider and spend limits), #158 (mode C now or later). All are in P2; ask the human before starting them.

## 6. Review checklist (every PR)
- **Check out locally.** Use a detached worktree; for Jules PRs, run `git fetch origin pull/<n>/head:prN` first.
- **Diff against the merge base, not `main`.** Jules branches from older `main`, so a diff against `main` shows false reverts.
- **Scope and debris:**
  - Only files in scope.
  - Delete `.orig`, `.rej`, `patch_*.diff`/`patch_*.patch`, `test.go`, `wait_test.go`, `test-results/` and binaries. Jules leaves these on almost every task.
- **Go:**
  - `go build ./...`
  - `bin/golangci-lint-v2.14.0 run ./...` (and with `--build-tags=integration` when tagged files change)
  - `go test -race -count=1` on the touched packages
  - `go.mod` unchanged unless the issue allows otherwise
- **Web:**
  - `npm run lint`, `npm run build` and Playwright.
  - Playwright needs `libnspr4`/`libnss3`/`libasound2`. They are extracted (not installed) under the scratchpad `debs/x/usr/lib/x86_64-linux-gnu`; run `LD_LIBRARY_PATH=<that> npx playwright test`. For `node_modules`, symlink a sub-agent worktree's `web/node_modules`, or `npm ci`.
  - **Grep the Playwright log for console warnings.** On #197 this caught a shared `ReactFlowProvider` bug that the tests missed.
- **Probe:** break one behavior and confirm a test fails.
- **Before merging, confirm the PR head is the commit you reviewed.** Jules sometimes pushes again after `COMPLETED`; on #201 it pushed an empty commit.
- **Merge and close out:**
  - `gh pr merge <n> --squash --delete-branch`.
  - Tick the issue's boxes and the parent's split checklist.
  - Edit the #67 body (fetch it first), then post a comment.

## 7. Lessons learned
1. **Jules on P1 so far:** reliable at planning (a usable plan within 5–13 min, every time). It delivered #193, #197 and #198. It failed or stalled on #185, #186, #187 and #194, and is slow on #195. Typical delivery takes 8–70 min after plan approval.
2. **The REST activities feed lags up to 15+ min.** Messages can appear late, and state `AWAITING_USER_FEEDBACK` means Jules is waiting on us. Use the **Jules CLI** (`remote list --session`) as the liveness signal. Never count a miss without checking the CLI, open PRs, and the latest change-set file list. Jules often answers by pushing a PR rather than sending a message.
3. **Read the newest activities, not the oldest.** A script bug once hid a Jules question on #187.
4. **Avoid mid-work messages** unless Jules is stuck. A nudge is right after a real miss; otherwise flag debris at PR review.
5. **Give Jules exact specs:** real error JSON, real report shapes, exact test ids and numbers, and the fixture-generation commands. Generate the real values yourself first with the `gargi` CLI (for example `--emit-best`, or `--out report.json` with `reports: [...]`).
6. **Sub-agents can't push and have no `gh`.** The orchestrator verifies in a detached worktree, then pushes `<sha>:refs/heads/<branch>` and opens the PR. A branch checked out in an agent worktree can't be checked out elsewhere. A dead sub-agent's uncommitted work can be saved as a WIP commit and resumed by a fresh agent (#141).
7. **Fallback briefs:** paste the full issue text into a scratchpad file and point the agent at it. Include known pitfalls, such as the `httptest` cleanup order that caused #187's panic: `t.Cleanup` runs LIFO, so register `ts.Close` first and `runner.Close` second.
8. **Shell gotcha:** backticks inside double-quoted `printf` arguments are executed. Write #67 comments with a quoted heredoc (`<<'EOF'`).
9. **Known flaky tests:** `internal/orchestrator` `TestGraphExecutionWorkflow_Linear_Diamond`. The `cmd/server` `TestRun_Server` race was fixed in #141.
