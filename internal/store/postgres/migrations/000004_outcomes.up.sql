CREATE TABLE IF NOT EXISTS sim_outcomes (
    execution_id TEXT COLLATE "C" NOT NULL,
    graph_id TEXT COLLATE "C" NOT NULL,
    policy_version TEXT COLLATE "C" NOT NULL,
    latents JSONB NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (execution_id)
);

CREATE INDEX IF NOT EXISTS idx_sim_outcomes_graph_policy ON sim_outcomes (graph_id, policy_version);
