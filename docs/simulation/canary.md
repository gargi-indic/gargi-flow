# Progressive Delivery & Canary Rollout (Case 3)

Case 3 demonstrates sequential observation and multi-objective optimization (Pareto frontier) for automated release management: balancing deployment velocity (lead time) against blast radius (bad user-minutes) when releasing software into production.

This case study is implemented in [`examples/simulation/canary`](../../examples/simulation/canary) and bundled as the embedded template `canary-rollout` (alias `canary`).

---

## 1. Decision Problem

When deploying a new service version across phased traffic tiers (1% → 10% → 100%), automated gates decide whether to **promote** to the next stage or **rollback** immediately based on observed metric degradation (e.g. error rate anomaly).

- **Velocity (Speed)**: Promoting quickly minimizes overall deployment lead time.
- **Safety**: Premature promotion of a bad release exposes more users to anomalies, accumulating bad user-minutes.
- **Sequential Observation**: At 1% traffic, anomaly signals are noisy; at 10% traffic, statistical certainty increases. Gating policies trade off early termination versus observing longer.

---

## 2. Model Structure

### Workflow Graph (`graph.yaml`)

```text
[deploy_1]  (1% canary deployment)
    │
    ▼
[observe_1] (collect metrics at 1%)
    │
    ▼
[gate_1]    (decision: promote | rollback)
    │
    ├─── rollback ───► [rollback] ──► (end)
    │
    └─── promote  ───► [deploy_10] (10% traffic stage)
                           │
                           ▼
                       [observe_2] (collect metrics at 10%)
                           │
                           ▼
                       [gate_2]    (decision: promote | rollback)
                           │
                           ├─── rollback ───► [rollback] ──► (end)
                           │
                           └─── promote  ───► [deploy_100] (100% full rollout)
```

### Simulation Model (`sim.yaml`)

- **Latents**:
  - `is_bad`: Bernoulli distribution with parameter `world.p_bad`.
  - `anomaly_signal`: Latent true error rate when `is_bad == 1`.
- **Worlds**:
  - `low_risk_service`: $P(\text{bad release}) = 0.05$. Internal/non-critical service.
  - `payments_service`: $P(\text{bad release}) = 0.20$. High-risk, financial transaction service.
- **Objectives & Metrics**:
  - `lead_time`: Total duration of the deployment pipeline in minutes.
  - `bad_user_mins`: Cumulative bad traffic received by users during bad releases.
  - `cost`: Weighted composite objective minimizing both lead time and bad user exposure.

---

## 3. Parameter Policy & Grid Search

Gating decisions use threshold parameters $\tau_1$ and $\tau_2$:

```yaml
# policies/canary-grid.yaml
apiVersion: policy/v1
id: canary-rollout
version: grid
decisions:
  gate_1:
    rules:
      - when: "nodes.observe_1.outputs.error_rate > params.tau1"
        choose: rollback
      - else: promote
  gate_2:
    rules:
      - when: "nodes.observe_2.outputs.error_rate > params.tau2"
        choose: rollback
      - else: promote
parameters:
  tau1: [0.02, 0.06, 0.10, 0.14]
  tau2: [0.02, 0.06, 0.10, 0.14]
```

Grid search evaluates all $4 \times 4 = 16$ candidate parameter policies across both worlds.

---

## 4. CLI Usage

### Initializing from Template

Create a new directory initialized with the canary rollout model:

```bash
gargi model init --template canary ./my-canary-rollout
cd ./my-canary-rollout
```

### Validating the Model

Validate model semantics and node schemas:

```bash
gargi model validate sim.yaml
```

Output:
```text
OK
```

### Running the Experiment

Run Monte Carlo simulation across all candidate parameter policies and compute the Pareto frontier:

```bash
gargi sim run --local experiment.yaml
```

Example Output:
```text
Loaded experiment: canary-rollout
Worlds: low_risk_service, payments_service
Policies evaluated: 16
Evaluator: monte_carlo (runs=100)

Pareto Frontier (lead_time:min vs bad_user_mins:min):
[low_risk_service]
  - canary-rollout@grid[tau1=0.02,tau2=0.02]: lead_time=52.3m, bad_user_mins=0.20
  - canary-rollout@grid[tau1=0.14,tau2=0.14]: lead_time=18.0m, bad_user_mins=1.85
[payments_service]
  - canary-rollout@grid[tau1=0.02,tau2=0.02]: lead_time=46.0m, bad_user_mins=2.00
  - canary-rollout@grid[tau1=0.14,tau2=0.14]: lead_time=18.0m, bad_user_mins=7.40
```

---

## 5. Agent Walkthrough over MCP

AI agents can create, validate, and optimize canary rollout policies autonomously using the Gargi Model Context Protocol (MCP) server.

### 1. Template Discovery
```json
{
  "name": "list_templates",
  "arguments": {}
}
```
The agent inspects available templates and identifies `canary-rollout` (`canary`).

### 2. Model Creation
```json
{
  "name": "create_model",
  "arguments": {
    "id": "canary-prod",
    "template": "canary"
  }
}
```
Initializes the workspace model with `graph.yaml`, `sim.yaml`, and `policies/canary-grid.yaml`.

### 3. Model Validation
```json
{
  "name": "validate_model",
  "arguments": {
    "id": "canary-prod"
  }
}
```
Returns `{"errors": []}`, confirming semantic validity.

### 4. Running Experiment & Extracting Pareto Frontier
```json
{
  "name": "run_experiment",
  "arguments": {
    "model_id": "canary-prod",
    "experiment": "apiVersion: experiment/v1\nworlds: [low_risk_service, payments_service]\npolicies: {search: grid, policy: canary-grid.yaml}\nevaluator: monte_carlo\nruns: 100\nseed: 42\nreports: [regret, \"pareto:lead_time:min,bad_user_mins:min\"]\n"
  }
}
```

### 5. Fetching Pareto Report
```json
{
  "name": "get_report",
  "arguments": {
    "id": "<experiment_id>",
    "kind": "pareto"
  }
}
```
The agent receives the non-dominated Pareto set of policies along the speed vs. safety boundary, allowing it to select optimal gating thresholds for different risk profiles.
