CREATE TABLE IF NOT EXISTS policies (
    id VARCHAR(255) NOT NULL,
    graph_id VARCHAR(255) NOT NULL,
    version VARCHAR(255) NOT NULL,
    document BYTEA NOT NULL,
    format VARCHAR(255) NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL,
    PRIMARY KEY (graph_id, id, version)
);

CREATE TABLE IF NOT EXISTS policy_deployments (
    graph_id VARCHAR(255) NOT NULL,
    policy_id VARCHAR(255) NOT NULL,
    policy_version VARCHAR(255) NOT NULL,
    mode VARCHAR(50) NOT NULL,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL,
    PRIMARY KEY (graph_id),
    CONSTRAINT fk_policy_deployment_policy FOREIGN KEY (graph_id, policy_id, policy_version) REFERENCES policies(graph_id, id, version) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_policies_graph_id ON policies (graph_id);
