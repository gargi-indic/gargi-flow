# Orchestrator Handover: gargi-flow

Snapshot refreshed on **2026-10-24 (~19:16 UTC)**, end of **Epic #136 (Simulation P1)**: 10 of 11 top-level tasks closed (#137–#146), active on #147 (canary rollout, golden test & walkthrough) to complete Epic #136.
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
| 10 | #146 canvas Simulate + Results | ✅ closed | #204 (#208), #205 (#209), #206 (#210) |
| 11 | #147 canary rollout template, golden test, agent walkthrough | 🛠 active | Claude |

**Epic #136 exit criterion:** an agent connected over MCP builds the canary-rollout case (case 3 in ARCHITECTURE.md) from a natural-language prompt, runs it, and returns a Pareto set of policies, all visible on the canvas.

## 4. Current state and immediate resume steps (2026-10-24, ~19:16 UTC)
0. **#146 closed!** PR #208 (#204), PR #209 (#205), and PR #210 (#206) merged. Full canvas Simulate and Results mode complete.
1. **Active task:** #147 Case 3 canary rollout template, golden test and agent walkthrough.
2. **Jules backlog for Epic #136 is complete.** #147 is assigned to Claude Code sub-agent.
3. **Scope of #147:**
   - `examples/simulation/canary/` per architecture §11 case 3.
   - Statistical golden test of Pareto set under fixed seeds.
   - `docs/simulation/canary.md` and walkthrough where agent builds case over MCP.
   - `gargi model init --template canary|loan`.
   - Closes Epic #136.

