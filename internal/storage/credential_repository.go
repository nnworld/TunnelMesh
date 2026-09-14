package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const credentialColumns = `id,owner_user_id,name,credential_type,public_key,fingerprint,secret_ciphertext,secret_nonce,secret_key_id,secret_version,enabled,deleted_at,created_at,updated_at`

type credentialRepo struct{ db *sql.DB }

func NewCredentialRepository(db *sql.DB) CredentialRepository {
	return &credentialRepo{db: db}
}

func (r *credentialRepo) Create(ctx context.Context, credential Credential) (Credential, error) {
	if err := validateCredential(credential); err != nil {
		return Credential{}, err
	}
	credential.ID, credential.CreatedAt, credential.UpdatedAt = stamp(credential.ID, credential.CreatedAt, credential.UpdatedAt, "credential")
	_, err := r.db.ExecContext(ctx, `INSERT INTO credentials(`+credentialColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		credential.ID, credential.OwnerUserID, credential.Name, string(credential.Type), credential.PublicKey,
		credential.Fingerprint, credential.SecretCiphertext, credential.SecretNonce, credential.SecretKeyID, credential.SecretVersion,
		boolInt(credential.Enabled), nullableTime(credential.DeletedAt),
		tm(credential.CreatedAt), tm(credential.UpdatedAt))
	if err != nil {
		return Credential{}, err
	}
	return credential, nil
}

func (r *credentialRepo) Get(ctx context.Context, id string) (Credential, error) {
	if id == "" {
		return Credential{}, sql.ErrNoRows
	}
	return scanCredential(r.db.QueryRowContext(ctx, `SELECT `+credentialColumns+` FROM credentials WHERE id=?`, id))
}

func (r *credentialRepo) Update(ctx context.Context, credential Credential) error {
	if err := validateCredential(credential); err != nil {
		return err
	}
	if credential.UpdatedAt.IsZero() {
		credential.UpdatedAt = time.Now().UTC()
	}
	res, err := r.db.ExecContext(ctx, `UPDATE credentials SET owner_user_id=?,name=?,credential_type=?,public_key=?,fingerprint=?,secret_ciphertext=?,secret_nonce=?,secret_key_id=?,secret_version=?,enabled=?,deleted_at=?,updated_at=? WHERE id=?`,
		credential.OwnerUserID, credential.Name, string(credential.Type), credential.PublicKey, credential.Fingerprint,
		credential.SecretCiphertext, credential.SecretNonce, credential.SecretKeyID, credential.SecretVersion,
		boolInt(credential.Enabled), nullableTime(credential.DeletedAt), tm(credential.UpdatedAt), credential.ID)
	return checkAffected(res, err)
}

func (r *credentialRepo) Delete(ctx context.Context, id string, when time.Time) error {
	when = timeOrNow(when)
	res, err := r.db.ExecContext(ctx, `UPDATE credentials SET deleted_at=CASE WHEN deleted_at IS NULL THEN ? ELSE deleted_at END,enabled=0,updated_at=? WHERE id=?`, tm(when), tm(when), id)
	return checkAffected(res, err)
}

func (r *credentialRepo) Restore(ctx context.Context, id string) error {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `UPDATE credentials SET deleted_at=NULL,updated_at=? WHERE id=?`, tm(now), id)
	return checkAffected(res, err)
}

func (r *credentialRepo) List(ctx context.Context, filter CredentialFilter, cursor string, limit int) (Page[Credential], error) {
	cursor, limit = pageArgs(cursor, limit)
	conditions, args := credentialConditions(filter)
	if id := decodeCursor(cursor); id != "" {
		conditions = append(conditions, `id>?`)
		args = append(args, id)
	}
	query := `SELECT ` + credentialColumns + ` FROM credentials`
	if len(conditions) > 0 {
		query += ` WHERE ` + strings.Join(conditions, ` AND `)
	}
	query += ` ORDER BY id LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return Page[Credential]{}, err
	}
	defer rows.Close()
	page := Page[Credential]{}
	for rows.Next() {
		credential, err := scanCredential(rows)
		if err != nil {
			return Page[Credential]{}, err
		}
		page.Items = append(page.Items, credential)
	}
	if err := rows.Err(); err != nil {
		return Page[Credential]{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.HasMore = true
		page.NextCursor = encodeCursor(page.Items[len(page.Items)-1].ID)
	}
	return page, nil
}

func validateCredential(credential Credential) error {
	if credential.OwnerUserID == "" || strings.TrimSpace(credential.Name) == "" {
		return errors.New("credential owner and name are required")
	}
	switch credential.Type {
	case CredentialTypeSSHPublicKey:
		if strings.TrimSpace(credential.PublicKey) == "" || strings.TrimSpace(credential.Fingerprint) == "" {
			return errors.New("credential public key and fingerprint are required")
		}
	case CredentialTypePassword:
		// Password credentials intentionally have no public key or fingerprint.
	case CredentialTypeProxyBasic:
		// public_key carries the proxy username and fingerprint its hash, so a
		// row without either cannot be matched against a Basic header. The
		// password itself only ever exists as sealed ciphertext.
		if strings.TrimSpace(credential.PublicKey) == "" || strings.TrimSpace(credential.Fingerprint) == "" {
			return errors.New("credential username and fingerprint are required")
		}
		if credential.SecretCiphertext == "" {
			return errors.New("proxy credential requires an encrypted password")
		}
	default:
		return errors.New("unsupported credential type")
	}
	return nil
}

func credentialConditions(filter CredentialFilter) ([]string, []any) {
	conditions := make([]string, 0, 4)
	args := make([]any, 0, 5)
	if filter.OwnerUserID != "" {
		conditions = append(conditions, `owner_user_id=?`)
		args = append(args, filter.OwnerUserID)
	}
	if filter.Type != "" {
		conditions = append(conditions, `credential_type=?`)
		args = append(args, string(filter.Type))
	}
	switch filter.Status {
	case CredentialStatusDeleted:
		conditions = append(conditions, `deleted_at IS NOT NULL`)
	case CredentialStatusAll:
	case CredentialStatusActive, "":
		conditions = append(conditions, `deleted_at IS NULL`)
	default:
		conditions = append(conditions, `1=0`)
	}
	if filter.Keyword != "" {
		keyword := boundedAgentFilter(filter.Keyword)
		conditions = append(conditions, `(INSTR(name, ?)>0 OR INSTR(public_key, ?)>0 OR INSTR(fingerprint, ?)>0)`)
		args = append(args, keyword, keyword, keyword)
	}
	return conditions, args
}

func scanCredential(row rowScanner) (Credential, error) {
	var credential Credential
	var credentialType string
	var deleted, created, updated sql.NullString
	var enabled int
	var secretCiphertext, secretNonce, secretKeyID sql.NullString
	// secret_version stays NULL for rows created before schema v13 and for
	// credentials that carry no secret, so it must be scanned as nullable.
	var secretVersion sql.NullInt64
	if err := row.Scan(&credential.ID, &credential.OwnerUserID, &credential.Name, &credentialType, &credential.PublicKey,
		&credential.Fingerprint, &secretCiphertext, &secretNonce, &secretKeyID, &secretVersion,
		&enabled, &deleted, &created, &updated); err != nil {
		return Credential{}, err
	}
	credential.SecretCiphertext, credential.SecretNonce, credential.SecretKeyID = secretCiphertext.String, secretNonce.String, secretKeyID.String
	credential.SecretVersion = int(secretVersion.Int64)
	credential.Type = CredentialType(credentialType)
	credential.Enabled = enabled != 0
	credential.DeletedAt = parseTM(deleted)
	credential.CreatedAt = parseTime(created.String)
	credential.UpdatedAt = parseTime(updated.String)
	return credential, nil
}
