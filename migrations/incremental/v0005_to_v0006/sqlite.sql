ALTER TABLE users ADD COLUMN deleted_at TEXT;
CREATE INDEX idx_users_role_deleted ON users(role, deleted_at, id);
