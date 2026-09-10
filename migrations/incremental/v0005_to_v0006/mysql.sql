ALTER TABLE users ADD COLUMN deleted_at VARCHAR(32) NULL AFTER disabled;
CREATE INDEX idx_users_role_deleted ON users(role, deleted_at, id);
