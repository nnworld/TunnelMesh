ALTER TABLE credentials ADD COLUMN secret_ciphertext TEXT;
ALTER TABLE credentials ADD COLUMN secret_nonce TEXT;
ALTER TABLE credentials ADD COLUMN secret_key_id VARCHAR(64);
ALTER TABLE credentials ADD COLUMN secret_version INTEGER;
