# Insurance Claims Triage with AI Agent (Case 2)

Case 2 demonstrates coupled simulation (Mode B) featuring stochastic Poisson arrivals, shared resource pooling with queueing dynamics, multi-agent policy tournaments, and multi-objective Pareto optimization under normal operations versus catastrophe surge conditions.

This case study is implemented in [`examples/simulation/claims`](../../examples/simulation/claims) and bundled as the embedded template `claims-triage` (alias `claims`).

---

## 1. Decision Problem

Property and casualty insurance carriers process thousands of inbound loss claims daily. While straightforward claims can be processed rapidly via straight-through auto-payment, suspicious or complex claims require manual adjuster investigation to avoid fraud losses:

- **Auto-Payment vs Fraud Leakage**: Sending claims with lower AI confidence straight to auto-pay speeds up cycle time and saves expensive manual adjuster costs (\$500/claim), but risks paying fraudulent claims (fraud rate $\approx 5\%$, claim amount lognormally distributed $\mu=7.5, \sigma=1.0$).
- **Adjuster Staffing vs Queueing Delay**: Human adjusters have finite capacity (e.g. 10 claims concurrently, each requiring $\approx 20$ hours mean review time). When claim arrival rates surge (e.g., severe weather catastrophe), claims queue up, ballooning cycle time.
- **SLA Penalty**: Regulatory and customer SLAs mandate claim resolution within 5 days (120 hours). Breaching 120 hours incurs a steep penalty (\$1,000 per violation).
- **Core Optimization Question**: What confidence threshold $\theta$ should route claims to auto-pay versus human adjusters, and how does policy ranking hold up between a `normal` week and a 5× `cat_surge`?

---

## 2. Model Structure

### Workflow Graph (`graph.yaml`)

```text
[intake]
    │
    ▼
[ai_assess] (automated fraud & confidence assessment)
    │
    ▼
[route]     (decision: auto_pay | adjuster | fraud_unit)
    │
    ├─── auto_pay ────────► [payout] ────► (end)
    │
    ├─── fraud_unit ──────► [fraud_unit] ─► (end)
    │
    └─── adjuster ────────► [adjuster] (shared resource queue)
                                │
                                ├─── approved ──► [payout]
                                │
                                └─── flagged  ──► [fraud_unit]
```

### Simulation Model (`sim.yaml`)

- **Arrivals (Mode B)**:
  - Inbound Poisson arrival process with rate parameterized by world:
    - `normal`: `arrival_rate = 10`
    - `cat_surge`: `arrival_rate = 50` (5× catastrophe surge)
  - Horizon: 10 days (`240h`).
- **Latents**:
  - `fraud`: Bernoulli distribution with $p = 0.05$.
  - `amount`: Lognormal distribution ($\mu = 7.5, \sigma = 1.0$), representing the dollar claim value.
  - `score`: Uniform distribution ($[0.0, 1.0]$), driving assessment signals.
- **Shared Resources**:
  - `adjuster`: Capacity 10 pooled human adjusters. Claims entering the `adjuster` node acquire a resource slot or queue until an adjuster becomes free.
- **Behaviors**:
  - `ai_assess`: Cost \$1.50, duration 10s. Evaluates synthetic fraud likelihood to emit a continuous confidence score: higher for legitimate claims ($\approx 0.70 - 0.98$), lower for fraudulent claims ($\approx 0.20 - 0.50$).
  - `adjuster`: Cost \$500, review duration lognormally distributed ($\mu = 11.2, \sigma = 0.5$ in seconds, corresponding to $\approx 20$ hours mean review time).
- **Objectives & Metrics**:
  - `total_cost`: Sum of operational processing costs, paid fraud loss leakage, and SLA violation penalties ($>\text{120h}$).
  - `p95_duration`: 95th percentile cycle time in hours.
  - `objective`: Maximizes negative total cost (equivalent to minimizing total cost).

---

## 3. Evaluated Policies & Agent Tournament

Three distinct policy candidates are compared:

1. **`rules-baseline` (`policies/rules.yaml`)**:
   - Threshold $\theta = 0.90$.
   - Aggressive auto-payment: routes claims with confidence $\ge 0.90$ directly to `auto_pay`, $\le 0.30$ to `fraud_unit`, and remainder to `adjuster`.
   - Low review cost and short cycle time, but higher exposure to fraud leakage.

2. **`matrix-calibrated` (`policies/matrix.yaml`)**:
   - Threshold $\theta = 0.95$.
   - Calibrated policy balancing adjuster review cost against fraud prevention ($\le 0.40 \to$ `fraud_unit`, $\ge 0.95 \to$ `auto_pay`).

3. **`agent-policy` (`policies/agent.yaml`)**:
   - Threshold $\theta = 0.98$.
   - Cautious AI agent routing: requires $\ge 0.98$ confidence for auto-payment, sending dubious claims ($\le 0.50$) to `fraud_unit` and routing more borderline cases to adjuster review.

### Tournament & Pareto Frontier

- **Tournament Report**: Evaluates candidate policies pairwise under Common Random Numbers (CRN) across Monte Carlo replications. Computes mean difference, paired confidence intervals ($1 - \alpha = 0.95$), and head-to-head win rates.
- **Pareto Frontier**: Identifies non-dominated tradeoffs between total financial cost (`total_cost:min`) and completion time (`p95_duration:min`).

---

## 4. CLI Usage

### Initializing from Template

Create a new directory initialized with the claims triage template:

```bash
gargi model init --template claims ./my-claims-triage
cd ./my-claims-triage
```

### Validating the Model

Validate model semantics, graph connectivity, and CEL expressions:

```bash
gargi model validate sim.yaml
```

Output:
```text
OK
```

### Running the Experiment

Run Monte Carlo simulation and compute both the Pareto frontier and agent tournament rankings:

```bash
gargi sim run --local experiment.yaml
```

Example Output:
```text
Loaded experiment: claims-triage
Worlds: normal, cat_surge
Policies evaluated: 3
Evaluator: monte_carlo (runs=100)

Tournament Rankings:
  1. matrix-calibrated (mean EV: -3420.50)
  2. agent-policy      (mean EV: -3890.20)
  3. rules-baseline    (mean EV: -4610.80)

Pareto Frontier (total_cost:min vs p95_duration:min):
[normal]
  - rules-baseline@1:     total_cost=2150.40, p95_duration=18.5h
  - matrix-calibrated@1:  total_cost=2640.10, p95_duration=28.2h
[cat_surge]
  - matrix-calibrated@1:  total_cost=4200.80, p95_duration=74.1h
  - agent-policy@1:       total_cost=4580.30, p95_duration=82.6h
```

---

## 5. Agent Walkthrough over MCP

Autonomous AI agents can author, validate, and optimize claims triage models using the Gargi Model Context Protocol (MCP) server.

### 1. Template Discovery
```json
{
  "name": "list_templates",
  "arguments": {}
}
```
The agent inspects available templates and identifies `claims-triage` (`claims`).

### 2. Model Creation
```json
{
  "name": "create_model",
  "arguments": {
    "id": "claims-triage-prod",
    "template": "claims"
  }
}
```
Initializes the workspace model with `graph.yaml`, `sim.yaml`, `experiment.yaml`, and the candidate policies in `policies/`.

### 3. Model Validation
```json
{
  "name": "validate_model",
  "arguments": {
    "id": "claims-triage-prod"
  }
}
```
Returns `{"errors": []}`, confirming semantic validity.

### 4. Running Experiment
```json
{
  "name": "run_experiment",
  "arguments": {
    "model_id": "claims-triage-prod",
    "experiment": "apiVersion: experiment/v1\nworlds: [normal, cat_surge]\npolicies:\n  files:\n    - rules.yaml\n    - matrix.yaml\n    - agent.yaml\nevaluator: monte_carlo\nruns: 100\nseed: 42\nreports:\n  - \"pareto:total_cost:min,p95_duration:min\"\n  - \"tournament\"\nworld_prior:\n  normal: 0.8\n  cat_surge: 0.2\n"
  }
}
```

### 5. Fetching Reports
Fetch the Pareto frontier:
```json
{
  "name": "get_report",
  "arguments": {
    "id": "<experiment_id>",
    "kind": "pareto"
  }
}
```

Fetch the Agent Tournament results:
```json
{
  "name": "get_report",
  "arguments": {
    "id": "<experiment_id>",
    "kind": "tournament"
  }
}
```
The agent receives the tournament leaderboard with pairwise win rates and paired confidence intervals, allowing it to select the most robust operational policy under surge conditions.
