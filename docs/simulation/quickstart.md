# Simulation quickstart

This guide takes you from a fresh checkout to a solved decision problem with
the `gargi` CLI: validate a simulation model, run an experiment, read the
report, change a world, and write your own policy. It uses the loan
underwriting example in [`examples/simulation/loan`](../../examples/simulation/loan)
(Case 1 of the [simulation architecture](ARCHITECTURE.md#case-1--loan-underwriting-decision-tree-information-worlds)).

Everything runs in-process: no server, store or Temporal is needed.

## 1. Install

You need Go 1.26 (see `go.mod`). From the repository root:

```bash
make build            # builds ./... and bin/gargi
bin/gargi version
```

```text
gargi dev
```

`make build` runs `go build -o bin/gargi ./cmd/gargi`. Without `make`, use
`go build -o bin/gargi ./cmd/gargi`, or replace `bin/gargi` with
`go run ./cmd/gargi` in every command below.

The example consists of four documents:

| File | Kind | What it holds |
|------|------|---------------|
| `graph.yaml` | `graph/v1` | the workflow: `screen` (buy the AI score or skip it) → `ai_score` → `route` (approve, reject or human review) → `human_review` → `final` (approve or reject) → `approve` / `reject` |
| `sim.yaml` | `sim/v1` | worlds, the latent applicant type, node behaviors (costs, durations, outputs) and the objective (profit) |
| `policies/recession-v1.yaml` | `policy/v1` | a hand-written policy |
| `experiment.yaml` | `experiment/v1` | which worlds and policies to evaluate, and how |

## 2. Validate the model

```bash
bin/gargi model validate examples/simulation/loan/sim.yaml
```

```text
OK
```

On an invalid model `validate` prints one `path: code: message` line per
problem and exits 1; `--json` prints `{"errors":[...]}` instead.

## 3. Run the experiment

```bash
time go run ./cmd/gargi sim run examples/simulation/loan/experiment.yaml --local
# same as: time bin/gargi sim run examples/simulation/loan/experiment.yaml --local
```

`experiment.yaml` enumerates every deterministic policy of the graph
(`policies: {search: exhaustive}`, 42 policies), evaluates each one in both
worlds (`base`, `recession`) with the exact evaluator **and** with 200,000
Monte Carlo runs (`evaluator: both`, `runs: 200000`, `seed: 42`), and prints
the top 5 per world. That is 42 x 2 x 200,000 = 16.8 million simulated
episodes.

**This takes about 13 minutes.** Measured on an AMD Ryzen 7 4800H
(8 cores / 16 threads, 16 workers, WSL2), Go 1.26.0: `real 13m21.7s`,
`user 152m6.0s`, `sys 16m42.5s` (including the `go run` compile). There is
no progress output while it runs. The exact part finishes in under a second;
the rest is Monte Carlo (about 0.3 ms per episode per core, see
[benchmarks](../benchmarks.md#4-simulation-benchmarks)). Simulation runs
take time; the full run is the P0 exit criterion and the report below is
what it prints.

> **Faster alternative for iteration (about 1 second).** Run a temporary
> copy of the example with `evaluator: exact`. It enumerates every chance
> branch instead of sampling, so the means are exact and the best policies
> are the same:
>
> ```bash
> rm -rf /tmp/loan-quick && cp -r examples/simulation/loan /tmp/loan-quick
> sed -i 's/^evaluator: both$/evaluator: exact/' /tmp/loan-quick/experiment.yaml
> time bin/gargi sim run /tmp/loan-quick/experiment.yaml --local
> ```
>
> It prints the same tables with `evaluator exact` in the header, `MEAN`
> equal to `EXACT`, a zero-width `CI95`, and `real 0m0.342s` on the machine
> above.
>
> The exact evaluator needs a discrete model (no `normal`/`lognormal`
> distributions); Monte Carlo works on any model.

## 4. Reading the output

The full run prints (verbatim):

```text
World base (model loan-underwriting, evaluator both, 200000 runs, seed 42)
POLICY                                         MEAN   CI95            P95 DURATION  EXACT
loan-underwriting@exhaustive-b1b8bd20353bca7f  464.0  [458.7, 469.2]  0s            460.0
loan-underwriting@exhaustive-d4bf14c73a9be940  430.8  [425.5, 436.1]  48h0m0s       427.0
loan-underwriting@exhaustive-2e56031237224b11  428.4  [422.7, 434.2]  48h0m0s       424.2
loan-underwriting@exhaustive-6dc3a13c1fed194b  319.6  [317.0, 322.1]  48h0m0s       317.8
loan-underwriting@exhaustive-06bd241bd6e08a40  304.0  [300.5, 307.5]  48h0m0s       302.0
Best policy: screen=score; route[score,high]=reject; route[score,low]=approve
Exact EV: 460.0

World recession (model loan-underwriting, evaluator both, 200000 runs, seed 42)
POLICY                                         MEAN   CI95            P95 DURATION  EXACT
loan-underwriting@exhaustive-6dc3a13c1fed194b  201.0  [197.5, 204.5]  48h0m0s       202.5
loan-underwriting@exhaustive-dcb151a83f6d37a5  154.5  [151.0, 158.1]  48h0m0s       156.0
loan-underwriting@exhaustive-06bd241bd6e08a40  71.2   [65.6, 76.8]    48h0m0s       72.2
loan-underwriting@exhaustive-6f4316b647da2065  51.2   [45.6, 56.8]    48h0m0s       52.3
loan-underwriting@exhaustive-fcb4fad3e6395701  0.0    [0.0, 0.0]      0s            0.0
Best policy: screen=score; route[score,high]=reject; route[score,low]=review; final[score,low,fail]=reject; final[score,low,pass]=approve
Exact EV: 202.5

real	13m21.676s
user	152m6.048s
sys	16m42.450s
```

For each world there is a table of the top policies (`--top K` changes how
many), sorted by expected objective, best first:

| Column | Meaning |
|--------|---------|
| `POLICY` | policy id: `<policy id>@<version>`. Exhaustive search names each enumerated policy `exhaustive-<hash of its choices>` |
| `MEAN` | Monte Carlo mean of the objective (profit per application, in dollars) over `runs` runs. With `evaluator: exact` it is the exact expectation |
| `CI95` | 95% confidence interval of `MEAN`. It collapses to a point for the exact evaluator |
| `P95 DURATION` | 95th percentile of simulated time per application (`48h0m0s` = more than 5% of applications wait for the 48 hour human review; `0s` = the policy never reviews) |
| `EXACT` | the exact expected objective, computed by enumerating every chance branch; `-` when the exact evaluator did not run. With `evaluator: both` the ranking uses this column |

`Best policy:` spells out the winning policy's choices at every
information set, and `Exact EV:` is its exact expected profit:

- `screen=score` - buy the $20 AI risk score;
- `route[score,high]=reject` - when the score says *high* risk, reject;
- `route[score,low]=approve` (base) or `route[score,low]=review`
  (recession) - when the score says *low* risk, approve directly in the base
  world, but send to human review in a recession;
- `final[score,low,pass]=approve`, `final[score,low,fail]=reject` - after a
  review, follow its result.

So the answer to "should we buy the score, and when should a human review?"
is: always buy the score, and add a review step for low-risk scores only in a
recession. Every `EXACT` value lies inside the Monte Carlo `CI95`, which is
what the golden test (section 7) checks in CI for the top policy of each
world.

Other output options: `--json` prints the full report (every estimate,
including standard deviation, min/max, P50/P95 and constraint probabilities),
`--out report.json` writes it to a file, and `--workers N` sets the worker
pool size (the report does not depend on it).

### Solving instead of enumerating: `search: exact`

Exhaustive search scores every policy, which stops scaling quickly (it is
capped at `max_policies`, 10,000 by default). `policies: {search: exact}`
instead solves each world's optimal policy by backward induction over the
information sets. Its cost grows with the number of chance/decision
histories (39 for the loan), not with the number of policies, so a model
with 10^18 policies still solves in under a second. It needs a discrete model
in which every decision observes what earlier decisions observed and chose
(perfect recall); otherwise it reports a `not_perfect_recall` error for each
decision that forgets. `max_policies` does not apply to it.

```bash
sed -i 's/^policies: {search: exhaustive}$/policies: {search: exact}/' /tmp/loan-quick/experiment.yaml
bin/gargi sim run /tmp/loan-quick/experiment.yaml --local
```

```text
World base (model loan-underwriting, evaluator exact)
POLICY                                         MEAN   CI95            P95 DURATION  EXACT
loan-underwriting@exhaustive-b1b8bd20353bca7f  460.0  [460.0, 460.0]  0s            460.0
loan-underwriting@exhaustive-6dc3a13c1fed194b  317.8  [317.8, 317.8]  48h0m0s       317.8
Best policy: screen=score; route[score,high]=reject; route[score,low]=approve
Exact EV: 460.0
Optimal policy (exact solver, 39 histories): screen=score; route[score,high]=reject; route[score,low]=approve
Optimal EV: 460.0
INFORMATION SET    REACH   CHOICE   ACTION VALUES
screen             1.0000  score    score=460.0 skip=302.0
route[score,high]  0.2200  reject   approve=-2201.8 reject=-20.0 review=-170.0
route[score,low]   0.7800  approve  approve=595.4 reject=-20.0 review=413.1

World recession (model loan-underwriting, evaluator exact)
...
Optimal EV: 202.5
...
```

The policy table lists every world's optimum evaluated in each world (here
the base optimum loses $65 in a recession). Below it, each information set
the optimal policy reaches shows its probability and the value of every
option there: the expected profit given that point is reached, the option is
taken and the optimal policy is followed afterwards. `--json` adds a
`solution` object per world with these values for every information set
(including the ones the optimum never reaches), and `--emit-best` writes the
solved policy. Undo the change with
`sed -i 's/^policies: {search: exact}$/policies: {search: exhaustive}/' /tmp/loan-quick/experiment.yaml`
before following the next sections.

## 5. Editing a world

Worlds are named parameter sets in `sim.yaml`:

```yaml
worlds:
  base: {p_bad: 0.20, loss: 4000}
  recession: {p_bad: 0.35, loss: 6000}
```

`p_bad` is the share of applicants who default and `loss` what a default
costs. Make the recession harsher (half of the applicants are bad) in the
fast copy from section 3, validate, and rerun:

```bash
sed -i 's/recession: {p_bad: 0.35, loss: 6000}/recession: {p_bad: 0.50, loss: 6000}/' /tmp/loan-quick/sim.yaml
bin/gargi model validate /tmp/loan-quick/sim.yaml
bin/gargi sim run /tmp/loan-quick/experiment.yaml --local --top 3
```

```text
OK
World base (model loan-underwriting, evaluator exact)
POLICY                                         MEAN   CI95            P95 DURATION  EXACT
loan-underwriting@exhaustive-b1b8bd20353bca7f  460.0  [460.0, 460.0]  0s            460.0
loan-underwriting@exhaustive-d4bf14c73a9be940  427.0  [427.0, 427.0]  48h0m0s       427.0
loan-underwriting@exhaustive-2e56031237224b11  424.2  [424.2, 424.2]  48h0m0s       424.2
Best policy: screen=score; route[score,high]=reject; route[score,low]=approve
Exact EV: 460.0

World recession (model loan-underwriting, evaluator exact)
POLICY                                         MEAN  CI95          P95 DURATION  EXACT
loan-underwriting@exhaustive-6dc3a13c1fed194b  99.2  [99.2, 99.2]  48h0m0s       99.2
loan-underwriting@exhaustive-dcb151a83f6d37a5  39.2  [39.2, 39.2]  48h0m0s       39.2
loan-underwriting@exhaustive-fcb4fad3e6395701  0.0   [0.0, 0.0]    0s            0.0
Best policy: screen=score; route[score,high]=reject; route[score,low]=review; final[score,low,fail]=reject; final[score,low,pass]=approve
Exact EV: 99.2
```

The base world is unchanged. In the harsher recession the same policy is
still best, but its expected profit drops from $202.5 to $99.2 per
application. You can also add a world (for example `boom: {p_bad: 0.10,
loss: 3000}`) and list it in the experiment's `worlds`.

## 6. Writing a policy

A `policy/v1` document chooses an option at each decision node of the graph
(`screen`, `route`, `final`). Decisions are either ordered `rules` (first
matching `when` expression wins, `else` is the fallback) or a `table` keyed by
observed values. The id must be the model id; `version` distinguishes
policies.

Start from the best policy the search found: `--emit-best DIR` writes each
world's best policy as `DIR/<world>.policy.yaml` (it works with
`policies: {search: exhaustive}` and `{search: exact}`):

```bash
cp examples/simulation/loan/sim.yaml /tmp/loan-quick/sim.yaml   # undo the world edit
bin/gargi sim run /tmp/loan-quick/experiment.yaml --local --top 1 --emit-best /tmp/loan-quick/best > /dev/null
ls /tmp/loan-quick/best
cat /tmp/loan-quick/best/recession.policy.yaml
```

```text
base.policy.yaml
recession.policy.yaml
# screen=score; route[score,high]=reject; route[score,low]=review; final[score,low,fail]=reject; final[score,low,pass]=approve
apiVersion: policy/v1
id: loan-underwriting
version: exhaustive-6dc3a13c1fed194b
decisions:
  final:
    table:
      - observed:
          ai_score.output.band: low
          human_review.output.result: fail
          screen.output.choice: score
        choose: reject
      - observed:
          ai_score.output.band: low
          human_review.output.result: pass
          screen.output.choice: score
        choose: approve
    default: approve
  route:
    table:
      - observed:
          ai_score.output.band: high
          screen.output.choice: score
        choose: reject
      - observed:
          ai_score.output.band: low
          screen.output.choice: score
        choose: review
    default: approve
  screen:
    rules:
      - else: score
```

Or write one by hand. This policy buys the score and reviews every
applicant:

```bash
cat > /tmp/loan-quick/policies/review-all.yaml <<'EOF'
apiVersion: policy/v1
id: loan-underwriting
version: review-all
decisions:
  screen:
    rules:
      - else: score
  route:
    rules:
      - else: review
  final:
    rules:
      - when: "human_review.output.result == 'pass'"
        choose: approve
      - else: reject
EOF
```

To evaluate specific policies instead of searching, list their files under
`policies.files` (paths are relative to the experiment file) and run:

```bash
cat > /tmp/loan-quick/compare.yaml <<'EOF'
apiVersion: experiment/v1
model: ./sim.yaml
worlds: [base, recession]
policies:
  files: [policies/recession-v1.yaml, policies/review-all.yaml, best/base.policy.yaml]
evaluator: exact
runs: 1
seed: 42
top: 3
EOF
bin/gargi sim run /tmp/loan-quick/compare.yaml --local
```

```text
World base (model loan-underwriting, evaluator exact)
POLICY                                         MEAN   CI95            P95 DURATION  EXACT
loan-underwriting@exhaustive-b1b8bd20353bca7f  460.0  [460.0, 460.0]  0s            460.0
loan-underwriting@recession-v1                 317.8  [317.8, 317.8]  48h0m0s       317.8
loan-underwriting@review-all                   282.0  [282.0, 282.0]  48h0m0s       282.0
Best policy: policy loan-underwriting version exhaustive-b1b8bd20353bca7f
Exact EV: 460.0

World recession (model loan-underwriting, evaluator exact)
POLICY                                         MEAN   CI95            P95 DURATION  EXACT
loan-underwriting@recession-v1                 202.5  [202.5, 202.5]  48h0m0s       202.5
loan-underwriting@review-all                   52.3   [52.3, 52.3]    48h0m0s       52.3
loan-underwriting@exhaustive-b1b8bd20353bca7f  -65.0  [-65.0, -65.0]  0s            -65.0
Best policy: policy loan-underwriting version recession-v1
Exact EV: 202.5
```

Reviewing everyone costs $150 per application and loses the good applicants
who abandon the 48 hour wait, so it trails the best policy in both worlds;
the base-tuned policy loses $65 per application in a recession (the $267.5
regret below). Use `evaluator: monte_carlo` (or `both`) with a `runs` count for
models that are not discrete.

### Tuning a threshold: `params` with grid and random search

A policy can declare tunable `params` and read them in rules as
`params.<name>`. Each param is a range (`min`, `max`, optional `step`) or a
`values` list; `default` (else `min` or the first value) is what a plain
`policies.files` run uses. Params are constants: rules still see only the
decision's observed paths.

The synthetic model in
[`internal/sim/experiment/testdata/threshold`](../../internal/sim/experiment/testdata/threshold)
reveals a uniform score `s` and pays `100*s - 40` on accept, so accepting
when `s >= theta` is worth `50(1 - theta²) - 40(1 - theta)`, best at
`theta = 0.4`:

```yaml
# policies/theta.yaml
params:
  theta: {min: 0, max: 1, step: 0.05}
decisions:
  act:
    rules:
      - when: "signal.output.score >= params.theta"
        choose: accept
      - else: reject
```

`policies: {search: grid, policy: ./policies/theta.yaml}` evaluates every
combination of the params (here 21 values of `theta`; more than
`max_policies` is an error), and
`policies: {search: random, policy: ..., samples: 50, seed: 7}` evaluates 50
random draws (`seed` defaults to the experiment seed; identical draws are
evaluated once). All candidates share common random numbers, and each is
named `<id>@<version>[name=value,...]`:

```bash
bin/gargi sim run internal/sim/experiment/testdata/threshold/experiment.yaml --local
```

```text
World base (model threshold, evaluator monte_carlo, 2000 runs, seed 7)
POLICY                       MEAN  CI95          P95 DURATION  EXACT
threshold@theta[theta=0.4]   18.1  [17.3, 19.0]  0s            -
threshold@theta[theta=0.35]  18.0  [17.1, 18.9]  0s            -
threshold@theta[theta=0.45]  18.0  [17.1, 18.9]  0s            -
Best policy: policy threshold version theta params theta=0.4
```

With `--json` every policy result also carries `"params": {"theta": 0.4}`.

### Metrics, requirements and decision reports

A model can declare extra per-run **metrics** (numeric CEL over `world`,
`latent`, `nodes` and `run`, like the objective) and aggregate
**requirements** that a policy must meet in a world to be feasible. Next to
the per-run `constraints`, which only report a probability, requirements
change the ranking: feasible policies always come first, and the "Best
policy" line is the best feasible one.

```yaml
# sim.yaml (additions)
objective:
  # ...
  requirements:
    fast: "p95(duration) <= duration('24h')"           # mean | p50 | p95 of a metric
    approvals: "prob(nodes.approve.status == 'Succeeded') >= 0.5"
metrics:
  wait_hours: "run.duration / 3600.0"
```

`objective`, `cost` and `duration` (seconds) are built-in metrics. Each
policy result in `--json` then carries `metrics` (one estimate per declared
metric), `requirements` (`name`, `value`, `op`, `bound`, `satisfied`) and
`feasible`.

An experiment can ask for decision-quality **reports** over the evaluated
policies, computed from the primary (exact, when available) estimates:

```yaml
# experiment.yaml (additions)
reports: [regret, robust, "pareto:cost:min,objective"]
world_prior: {base: 0.7, recession: 0.3}   # default: uniform
cvar_alpha: 0.05                           # default
```

- `regret`: the policy tuned for each world (its best feasible policy)
  evaluated in every world, with the regret against each world's best.
- `robust`: among policies feasible in every world, the best by expected
  value under `world_prior`, by worst-case mean (maximin), and by CVaR: the
  mean objective over the worst `cvar_alpha` probability mass of the
  prior-weighted mixture of the policy's outcomes in all worlds.
- `pareto:<x>[:min|max],<y>[:min|max]`: per world, the feasible policies
  not dominated on the means of two metrics (goals default to `max`).

For the loan model with `evaluator: exact`, the text output ends with:

```text
Regret (rows: policy tuned for a world; columns: value (regret) in each world)
TUNED FOR  POLICY                                         BASE           RECESSION      MAX REGRET
base       loan-underwriting@exhaustive-b1b8bd20353bca7f  460.0 (0.0)    -65.0 (267.5)  267.5
recession  loan-underwriting@exhaustive-6dc3a13c1fed194b  317.8 (142.2)  202.5 (0.0)    142.2

Robust (prior base=0.70 recession=0.30; CVaR alpha 0.05; 42 eligible policies)
CRITERION       POLICY                                         SCORE  EXPECTED VALUE  WORST CASE  CVAR
expected value  loan-underwriting@exhaustive-b1b8bd20353bca7f  302.5  302.5           -65.0       -5280.0
maximin         loan-underwriting@exhaustive-6dc3a13c1fed194b  202.5  283.2           202.5       -884.0
cvar            loan-underwriting@exhaustive-fcb4fad3e6395701  0.0    0.0             0.0         0.0
```

The expected-value pick is the base-tuned policy and the worst-case pick the
recession-tuned one, as in the architecture's case 1. The CVaR pick is
reject-all (CVaR 0): every other policy loses money on its worst 5% of
applications (the expected-value pick -5,280, the maximin pick -884). The
same blocks are in `--json` under `"reports"`.

### Reports that explain the winner

Four more reports explain why a policy wins. `voi`, `flip_point` and
`policy_tree` run the exact solver, so they need a discrete model;
`path_heatmap` works with any evaluator.

```yaml
reports: [voi:ai_score, flip_point:p_bad, path_heatmap, policy_tree]
```

- `voi:<node>`: the value of observing a node, per world. It is the
  optimal EV of the model minus the optimal EV of a copy in which no
  decision observes the node (every `observes` path starting with
  `<node>.` is removed) and running the node is free (its behavior cost is
  removed; outputs, duration and abandonment stay).
- `flip_point:<param>[:<lo>,<hi>]`: where the optimal policy of the first
  experiment world changes as one numeric world param is swept, every other
  param keeping that world's value. The default range is the param's
  minimum and maximum across the experiment's worlds. The base world is
  solved at 21 evenly spaced points (`sweep`, the data for a chart), and
  every change between neighbours is bisected to a width of 1e-6·(hi − lo)
  (`flips`: `from`/`to` describe the two policies, `left`/`right` hold the
  bracketing values, policy ids and EVs).
- `path_heatmap`: per world, the probability that each node runs (it
  started, whatever its outcome) and that each edge is taken (its target
  started through it: the source succeeded and the condition held) under
  the world's best policy. Exact and both use branch probabilities, Monte
  Carlo the fraction of the experiment's own replications. The outgoing
  edges of a decision carry exactly the decision's mass.
- `policy_tree`: per world, the optimal table policy as a tree. Roots are
  the first decisions; each information set lists every option with its
  value and the information sets that follow it, including those the
  optimal policy never reaches (`onPath: false`).

For the loan model the text output adds (excerpt):

```text
Value of information (optimal EV with the node observed minus without it, its cost removed)
WORLD      NODE      WITH   WITHOUT  VOI
base       ai_score  460.0  302.0    158.0
recession  ai_score  202.5  72.2     130.3

Flip points of p_bad (world base, swept from 0.2 to 0.35)
p_bad in [0.2966361, 0.2966362]:
  from loan-underwriting@exhaustive-b1b8bd20353bca7f (EV 257.1): screen=score; route[score,high]=reject; route[score,low]=approve
  to   loan-underwriting@exhaustive-6dc3a13c1fed194b (EV 257.1): screen=score; route[score,high]=reject; route[score,low]=review; final[score,low,fail]=reject; final[score,low,pass]=approve

Policy tree, world recession (policy loan-underwriting@exhaustive-6dc3a13c1fed194b, EV 202.5; optimal path)
screen (reach 1.0000) -> score (202.5)
  route[score,high] (reach 0.3100) -> reject (-20.0)
  route[score,low] (reach 0.6900) -> review (302.5)
    final[score,low,fail] (reach 0.1150) -> reject (-170.0)
    final[score,low,pass] (reach 0.3995) -> approve (646.0)
```

The VOI matches the prototype's. With the base world's loss of 4,000,
reviewing low scores beats approving them once `p_bad` exceeds
436.5/1471.5 ≈ 0.2966 (the two policies' EVs are 880 − 2100·p and
443.5 − 628.5·p). The heatmap (top edges per world) is printed too; the
JSON lists every node and edge in graph order.

## 7. Golden numbers and the prototype

The loan results are locked in CI by `TestLoanGolden`
(`internal/sim/experiment/golden_test.go`), part of the normal
`make test` / `go test ./...` run:

- `internal/sim/experiment/testdata/loan_golden.json` holds, for all 42
  policies in both worlds (sorted by world, then by policy description), the
  exact `ev`, `approvalRate` (probability that `approve` runs),
  `meanDurationSeconds` (expected simulated time) and `abandonRate`
  (probability that the applicant abandons the human review). The test recomputes them with the exact evaluator and
  compares to 1e-9.
- It recomputes the prototype's derived numbers to 0.1: the cross-world
  regret (the base-tuned policy in a recession loses $267.5 against the
  recession optimum; the recession-tuned policy in the base world loses
  $142.2) and the value of information of the AI score (best EV with the
  score minus best EV without it: $158.0 base, $130.3 recession).
- It runs 200,000 Monte Carlo episodes (seed 42) of the top policy of each
  world and requires the mean to be within 3 standard errors of the exact EV
  (20,000 episodes under the race detector, which `make test` enables).

After an intentional change to the model or the evaluator, regenerate the
golden file and review the diff:

```bash
go test ./internal/sim/experiment -run TestLoanGolden -update
git diff internal/sim/experiment/testdata/loan_golden.json
```

The golden numbers are cross-checked against an independent implementation,
the Python prototype
[`docs/simulation/prototypes/loan_sim.py`](prototypes/loan_sim.py) (standard
library only). It enumerates the same 42 policies, computes each one's exact
EV, expected review days and approval rate, and prints the top 4 per world,
the cross-world regret and the VOI:

```bash
python3 docs/simulation/prototypes/loan_sim.py
```

```text
policy space: 42 deterministic policies

== world: base ==
  EV    460.0  days 0.00  approve 78.0%  | D1=score, low->approve, high->reject
  EV    427.0  days 0.44  approve 78.0%  | D1=score, low->approve, high->review, high/pass->reject, high/fail->reject
  EV    424.2  days 0.44  approve 84.7%  | D1=score, low->approve, high->review, high/pass->approve, high/fail->reject
  EV    317.8  days 1.56  approve 48.5%  | D1=score, low->review, high->reject, low/pass->approve, low/fail->reject
  ... worst EV -864.8 | D1=score, low->review, high->review, low/pass->reject, low/fail->approve, high/pass->approve, high/fail->approve
  baseline D1=skip, none->approve: EV 0.0
  baseline D1=skip, none->reject: EV 0.0
  MC check best: mean 459.9  (n=200k, seed 42)
  MC P(loss on an application) 28.1%

== world: recession ==
  EV    202.5  days 1.38  approve 40.0%  | D1=score, low->review, high->reject, low/pass->approve, low/fail->reject
  EV    156.0  days 2.00  approve 40.0%  | D1=score, low->review, high->review, low/pass->approve, low/fail->reject, high/pass->reject, high/fail->reject
  EV     72.2  days 2.00  approve 46.7%  | D1=skip, none->review, none/pass->approve, none/fail->reject
  EV     52.2  days 2.00  approve 46.7%  | D1=score, low->review, high->review, low/pass->approve, low/fail->reject, high/pass->approve, high/fail->reject
  ... worst EV -2141.0 | D1=score, low->review, high->review, low/pass->reject, low/fail->approve, high/pass->approve, high/fail->approve
  baseline D1=skip, none->approve: EV -1450.0
  baseline D1=skip, none->reject: EV 0.0
  MC check best: mean 205.7  (n=200k, seed 42)
  MC P(loss on an application) 61.0%

== cross-world regret ==
  policy tuned for base      run in base     : EV    460.0  (regret 0.0)
  policy tuned for base      run in recession: EV    -65.0  (regret 267.5)
  policy tuned for recession run in base     : EV    317.8  (regret 142.2)
  policy tuned for recession run in recession: EV    202.5  (regret 0.0)

robust (70/30 prior): D1=score, low->approve, high->reject
  in base: EV 460.0
  in recession: EV -65.0
VOI of AI score in base: 158.0 per application (cost 20)
VOI of AI score in recession: 130.3 per application (cost 20)
```

Print the same rows from the golden file to compare. The two models differ
in one modelling detail: in the Go kernel an applicant who abandons the human
review ends the episode when the review starts, while the prototype counts
the full 2 day wait for them. So the prototype's `days` is the golden mean
duration / 86400 plus 2 x `abandonRate`:

```bash
python3 -c '
import json
g = json.load(open("internal/sim/experiment/testdata/loan_golden.json"))
for w in ("base", "recession"):
    rows = sorted((e for e in g["entries"] if e["world"] == w), key=lambda e: -e["ev"])
    print("==", w, "==")
    for e in rows[:4]:
        days = e["meanDurationSeconds"] / 86400 + 2 * e["abandonRate"]
        print("  EV %8.1f  days %.2f  approve %.1f%%  | %s" % (e["ev"], days, 100 * e["approvalRate"], e["policy"]))
'
```

```text
== base ==
  EV    460.0  days 0.00  approve 78.0%  | screen=score; route[score,high]=reject; route[score,low]=approve
  EV    427.0  days 0.44  approve 78.0%  | screen=score; route[score,high]=review; route[score,low]=approve; final[score,high,fail]=reject; final[score,high,pass]=reject
  EV    424.2  days 0.44  approve 84.7%  | screen=score; route[score,high]=review; route[score,low]=approve; final[score,high,fail]=reject; final[score,high,pass]=approve
  EV    317.8  days 1.56  approve 48.5%  | screen=score; route[score,high]=reject; route[score,low]=review; final[score,low,fail]=reject; final[score,low,pass]=approve
== recession ==
  EV    202.5  days 1.38  approve 40.0%  | screen=score; route[score,high]=reject; route[score,low]=review; final[score,low,fail]=reject; final[score,low,pass]=approve
  EV    156.0  days 2.00  approve 40.0%  | screen=score; route[score,high]=review; route[score,low]=review; final[score,high,fail]=reject; final[score,high,pass]=reject; final[score,low,fail]=reject; final[score,low,pass]=approve
  EV     72.2  days 2.00  approve 46.7%  | screen=skip; route[skip,nil]=review; final[skip,nil,fail]=reject; final[skip,nil,pass]=approve
  EV     52.3  days 2.00  approve 46.7%  | screen=score; route[score,high]=review; route[score,low]=review; final[score,high,fail]=reject; final[score,high,pass]=approve; final[score,low,fail]=reject; final[score,low,pass]=approve
```

The EV, days and approval columns agree row for row; the prototype's policy
descriptions (`D1=score, low->approve, high->reject`) map to the Go
`Describe` strings (`screen=score; route[score,high]=reject;
route[score,low]=approve`). `TestExact_LoanMatchesPrototype` and
`TestLoanGolden` also compare all 84 exact EVs with the prototype's values to
1e-9 (a value exactly halfway, such as 52.25, may round to a different last
digit in the two printouts). The prototype's own Monte Carlo check uses
Python's random generator, so its sampled means (459.9, 205.7) differ from
the Go run's (464.0, 201.0); both are within 2 standard errors of the exact
values.

## Next steps

- [Simulation architecture](ARCHITECTURE.md): the documents, kernel, run
  modes and roadmap.
- [Model Context Protocol (MCP) server](mcp.md): connect agents to build, repair and run decision models.
- [Benchmarks](../benchmarks.md#4-simulation-benchmarks): episode and
  experiment throughput.
