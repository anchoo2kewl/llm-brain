-- Latest observed quota per host and limit bucket; account identity is not reported by rollouts.
CREATE TABLE IF NOT EXISTS quota_observations (
 host TEXT NOT NULL,
 provider TEXT NOT NULL,
 limit_id TEXT NOT NULL,
 observed_at TEXT NOT NULL,
 payload TEXT NOT NULL,
 PRIMARY KEY (host, provider, limit_id)
);
