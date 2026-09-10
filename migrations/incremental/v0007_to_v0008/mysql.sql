ALTER TABLE server_nodes ADD COLUMN name VARBINARY(255) NOT NULL DEFAULT '';
ALTER TABLE server_nodes ADD COLUMN enabled BIGINT NOT NULL DEFAULT 1;
ALTER TABLE server_nodes ADD COLUMN deleted_at VARCHAR(32);
UPDATE server_nodes SET name=id WHERE name='';
