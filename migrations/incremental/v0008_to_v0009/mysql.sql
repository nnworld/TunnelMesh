CREATE TABLE IF NOT EXISTS authorization_revision (
    id INTEGER PRIMARY KEY,
    revision BIGINT UNSIGNED NOT NULL,
    updated_at TEXT NOT NULL
);
INSERT INTO authorization_revision(id, revision, updated_at) VALUES (1, 1, '1970-01-01T00:00:00Z');
