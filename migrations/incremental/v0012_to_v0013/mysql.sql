ALTER TABLE credentials
    ADD COLUMN secret_ciphertext TEXT NULL,
    ADD COLUMN secret_nonce TEXT NULL,
    ADD COLUMN secret_key_id VARCHAR(64) NULL,
    ADD COLUMN secret_version INTEGER NULL;
