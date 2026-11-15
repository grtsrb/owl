-- Schema v1 (migration 1; later migrations live in sqlite.go). Timestamps: fixed-width UTC strings.
CREATE TABLE IF NOT EXISTS clusters (
    id                 INTEGER PRIMARY KEY,
    name               TEXT NOT NULL UNIQUE,
    stage              TEXT NOT NULL CHECK (stage IN ('staging', 'prelive', 'live')),
    tenant             TEXT NOT NULL,
    created_at         TEXT NOT NULL,
    last_seen_at       TEXT,
    kubernetes_version TEXT NOT NULL DEFAULT '',
    agent_version      TEXT NOT NULL DEFAULT ''
);

-- Only SHA-256 hashes of agent tokens are stored; the token itself is shown once at creation.
CREATE TABLE IF NOT EXISTS agent_tokens (
    id           INTEGER PRIMARY KEY,
    cluster_id   INTEGER NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    token_hash   TEXT NOT NULL UNIQUE,
    prefix       TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    last_used_at TEXT,
    revoked_at   TEXT
);

-- Current state: replaced on every ingest.
CREATE TABLE IF NOT EXISTS current_images (
    cluster_id       INTEGER NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    namespace        TEXT NOT NULL,
    kind             TEXT NOT NULL,
    workload         TEXT NOT NULL,
    container        TEXT NOT NULL,
    image            TEXT NOT NULL,
    is_init          INTEGER NOT NULL DEFAULT 0,
    desired_replicas INTEGER NOT NULL DEFAULT 0,
    running          TEXT NOT NULL DEFAULT '[]', -- JSON []snapshot.RunningImage
    PRIMARY KEY (cluster_id, namespace, kind, workload, container)
);
CREATE INDEX IF NOT EXISTS idx_current_images_image ON current_images(image);

-- History: only changes, not full snapshots.
CREATE TABLE IF NOT EXISTS changes (
    id         INTEGER PRIMARY KEY,
    cluster_id INTEGER NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    at         TEXT NOT NULL,
    type       TEXT NOT NULL,
    namespace  TEXT NOT NULL DEFAULT '',
    kind       TEXT NOT NULL DEFAULT '',
    workload   TEXT NOT NULL DEFAULT '',
    container  TEXT NOT NULL DEFAULT '',
    old_value  TEXT NOT NULL DEFAULT '',
    new_value  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_changes_cluster_at ON changes(cluster_id, at);
