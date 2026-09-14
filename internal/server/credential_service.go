package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
	"golang.org/x/crypto/ssh"
)

var (
	ErrCredentialInvalid = errors.New("invalid ssh credential")
	ErrResourceForbidden = errors.New("resource is forbidden")
	// ErrCredentialSecretUnavailable means the process has no
	// TUNNELMESH_TOKEN_ENCRYPTION_KEY, so credential secrets cannot be stored
	// or decrypted. Creating a credential with a secret fails fast instead of
	// silently degrading to plaintext storage.
	ErrCredentialSecretUnavailable = errors.New("credential secret storage is unavailable: TUNNELMESH_TOKEN_ENCRYPTION_KEY is not configured")
)

const (
	maxSSHPrivateKeyBytes        = 64 << 10
	maxCredentialPasswordBytes   = 4 << 10
	maxCredentialPassphraseBytes = 1 << 10
	// maxCredentialUsernameBytes bounds the proxy username. It is stored in the
	// public_key column and echoed in every 407 challenge round-trip, so an
	// unbounded value would be an amplification vector.
	maxCredentialUsernameBytes = 256
)

type CredentialInput struct {
	Name      string
	Type      storage.CredentialType
	PublicKey string
	// Username is the proxy account name for CredentialTypeProxyBasic. It is
	// stored in the public_key column; see proxyBasicIdentity.
	Username string
	Enabled  bool
	Secret   *CredentialSecret
}

type CredentialPatch struct {
	Name      *string
	Type      *storage.CredentialType
	PublicKey *string
	// Username rewrites the proxy account name. nil means "unchanged"; a
	// non-nil blank value is rejected because the username is mandatory.
	Username *string
	Enabled  *bool
	Secret   *CredentialSecret
}

// CredentialSecret is the plaintext material a browser needs to complete SSH
// authentication without prompting the user. It is sealed into one AES-GCM
// JSON blob before it reaches storage and is never logged, listed, or echoed
// back by the credential APIs.
type CredentialSecret struct {
	Password   string
	PrivateKey string
	Passphrase string
}

// credentialSecretBlob is the on-disk JSON shape. One blob per credential keeps
// the migration, encryption path and future credential kinds in a single place.
type credentialSecretBlob struct {
	Password   string `json:"password,omitempty"`
	PrivateKey string `json:"privateKey,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
}

func (s CredentialSecret) isEmpty() bool {
	return s.Password == "" && s.PrivateKey == "" && s.Passphrase == ""
}

type ExtractedSSHPublicKey struct {
	PublicKey   string
	Fingerprint string
}

type CredentialListFilter = storage.CredentialFilter

type CredentialService struct {
	credentials storage.CredentialRepository
	agents      storage.AgentRepository
	audits      storage.AuditRepository
	secretStore *auth.SecretStore
}

func NewCredentialService(credentials storage.CredentialRepository, agents storage.AgentRepository, audits storage.AuditRepository) *CredentialService {
	service := &CredentialService{credentials: credentials, agents: agents, audits: audits}
	// Mirrors TokenService: the encryption key is injected by the environment
	// or a secret manager and is never persisted by this package.
	if encodedKey := strings.TrimSpace(os.Getenv("TUNNELMESH_TOKEN_ENCRYPTION_KEY")); encodedKey != "" {
		keyID := strings.TrimSpace(os.Getenv("TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID"))
		if keyID == "" {
			keyID = "default"
		}
		if store, err := auth.NewSecretStore(encodedKey, keyID); err == nil {
			service.secretStore = store
		}
	}
	return service
}

// SetSecretStore overrides the environment-derived store. Tests and embedders
// use it to supply a deterministic key.
func (s *CredentialService) SetSecretStore(store *auth.SecretStore) {
	if s != nil {
		s.secretStore = store
	}
}

func (s *CredentialService) Create(ctx context.Context, actor auth.Principal, input CredentialInput) (storage.Credential, error) {
	credentialType := input.Type
	if credentialType == "" {
		credentialType = storage.CredentialTypeSSHPublicKey
	}
	credential := storage.Credential{
		OwnerUserID: actor.UserID, Name: strings.TrimSpace(input.Name), Type: credentialType,
		Enabled: input.Enabled,
	}
	if credentialType == storage.CredentialTypeSSHPublicKey {
		normalized, fingerprint, err := ParseOpenSSHPublicKey(input.PublicKey)
		if err != nil {
			return storage.Credential{}, err
		}
		credential.PublicKey, credential.Fingerprint = normalized, fingerprint
	}
	if credentialType == storage.CredentialTypeProxyBasic {
		username, fingerprint, err := proxyBasicIdentity(input.Username)
		if err != nil {
			return storage.Credential{}, err
		}
		credential.PublicKey, credential.Fingerprint = username, fingerprint
	}
	if err := validateCredentialShape(credential, input.Secret, false); err != nil {
		return storage.Credential{}, err
	}
	if input.Secret != nil && !input.Secret.isEmpty() {
		if err := s.applySecret(&credential, *input.Secret); err != nil {
			return storage.Credential{}, err
		}
	}
	created, err := s.credentials.Create(ctx, credential)
	if err != nil {
		return storage.Credential{}, mapCredentialStorageError(err)
	}
	s.writeAudit(ctx, actor, "credential.created", created.ID)
	if created.HasSecret() {
		s.writeAudit(ctx, actor, "credential.secret-set", created.ID)
	}
	return created, nil
}

func (s *CredentialService) Get(ctx context.Context, actor auth.Principal, id string) (storage.Credential, error) {
	credential, err := s.credentials.Get(ctx, id)
	if err != nil {
		return storage.Credential{}, err
	}
	if !isAdmin(actor) && credential.OwnerUserID != actor.UserID {
		return storage.Credential{}, ErrResourceForbidden
	}
	return credential, nil
}

func (s *CredentialService) Update(ctx context.Context, actor auth.Principal, id string, input CredentialPatch) (storage.Credential, error) {
	current, err := s.Get(ctx, actor, id)
	if err != nil {
		return storage.Credential{}, err
	}
	if input.Name != nil {
		current.Name = strings.TrimSpace(*input.Name)
	}
	// A type change invalidates the stored secret shape, so the secret is
	// dropped unless the same request supplies a compatible replacement. This
	// avoids decrypting a secret just to inspect its kind.
	typeChanged := input.Type != nil && *input.Type != current.Type
	if input.Type != nil {
		current.Type = *input.Type
	}
	switch current.Type {
	case storage.CredentialTypePassword:
		current.PublicKey, current.Fingerprint = "", ""
	case storage.CredentialTypeProxyBasic:
		if typeChanged {
			// Switching into proxy_basic must not inherit an SSH public key as
			// the username, so the caller has to supply one explicitly.
			current.PublicKey, current.Fingerprint = "", ""
		}
	}
	if input.PublicKey != nil {
		if current.Type == storage.CredentialTypeSSHPublicKey {
			normalized, fingerprint, err := ParseOpenSSHPublicKey(*input.PublicKey)
			if err != nil {
				return storage.Credential{}, err
			}
			current.PublicKey, current.Fingerprint = normalized, fingerprint
		}
	}
	if input.Username != nil {
		// Only proxy credentials own a username, and blanking it is rejected so
		// a partial PATCH cannot leave an unmatchable row behind.
		if current.Type != storage.CredentialTypeProxyBasic {
			return storage.Credential{}, fmt.Errorf("%w: username applies to proxy credentials only", ErrCredentialInvalid)
		}
		username, fingerprint, err := proxyBasicIdentity(*input.Username)
		if err != nil {
			return storage.Credential{}, err
		}
		current.PublicKey, current.Fingerprint = username, fingerprint
	}
	if input.Enabled != nil {
		current.Enabled = *input.Enabled
	}
	storedSecret := current.HasSecret() && !typeChanged
	if typeChanged {
		current.SecretCiphertext, current.SecretNonce, current.SecretKeyID, current.SecretVersion = "", "", "", 0
	}
	if err := validateCredentialShape(current, input.Secret, storedSecret); err != nil {
		return storage.Credential{}, err
	}
	if input.Secret != nil && !input.Secret.isEmpty() {
		if err := s.applySecret(&current, *input.Secret); err != nil {
			return storage.Credential{}, err
		}
	}
	if err := s.credentials.Update(ctx, current); err != nil {
		return storage.Credential{}, mapCredentialStorageError(err)
	}
	updated, err := s.Get(ctx, actor, id)
	if err == nil {
		s.writeAudit(ctx, actor, "credential.updated", id)
		if input.Secret != nil && !input.Secret.isEmpty() {
			s.writeAudit(ctx, actor, "credential.secret-set", id)
		}
	}
	return updated, err
}

// GetWithSecret decrypts the stored secret for the credential owner. The
// plaintext is returned to the caller only; administrators can manage the
// credential row but never read another user's secret.
func (s *CredentialService) GetWithSecret(ctx context.Context, actor auth.Principal, id string) (storage.Credential, *CredentialSecret, error) {
	credential, err := s.credentials.Get(ctx, id)
	if err != nil {
		return storage.Credential{}, nil, err
	}
	if credential.OwnerUserID != actor.UserID {
		return storage.Credential{}, nil, ErrResourceForbidden
	}
	if !credential.HasSecret() {
		return credential, nil, nil
	}
	secret, err := s.openSecret(credential)
	if err != nil {
		return storage.Credential{}, nil, err
	}
	return credential, secret, nil
}

// ProxyBasicSecret decrypts one proxy credential for an authenticated caller.
// It is the management path: ownership is enforced exactly like every other
// credential read.
func (s *CredentialService) ProxyBasicSecret(ctx context.Context, actor auth.Principal, id string) (string, string, error) {
	credential, err := s.Get(ctx, actor, id)
	if err != nil {
		return "", "", err
	}
	return s.proxyBasicSecret(credential)
}

// ResolveProxyBasicSecret decrypts one proxy credential WITHOUT an ownership
// check. It exists solely for the managed HTTP proxy entry.
//
// The entry authenticates whoever supplied the Proxy-Authorization header; that
// person is not a TunnelMesh user and has no principal to compare against. The
// authorization decision was already made when an administrator attached this
// credential to a route, so the entry is allowed to read it. Management API
// handlers must never call this method -- use ProxyBasicSecret instead.
func (s *CredentialService) ResolveProxyBasicSecret(ctx context.Context, id string) (string, string, error) {
	credential, err := s.credentials.Get(ctx, id)
	if err != nil {
		return "", "", err
	}
	return s.proxyBasicSecret(credential)
}

// proxyBasicSecret is the single path that turns a stored blob back into a
// password. The value must never reach a log line, an audit record or a metric
// label.
func (s *CredentialService) proxyBasicSecret(credential storage.Credential) (string, string, error) {
	if credential.Type != storage.CredentialTypeProxyBasic || !credential.Enabled || credential.DeletedAt != nil {
		// Reported as ErrCredentialInvalid on purpose: the proxy entry maps
		// anything that is not a store failure to "bad credentials", which keeps
		// credential IDs unprobeable.
		return "", "", ErrCredentialInvalid
	}
	if !credential.HasSecret() {
		return "", "", ErrCredentialInvalid
	}
	secret, err := s.openSecret(credential)
	if err != nil {
		if errors.Is(err, ErrCredentialSecretUnavailable) {
			return "", "", err
		}
		// A corrupt blob or a rotated encryption key is an operator-side fault.
		// Surfacing it as "unavailable" stops the entry from counting the
		// attempt against the client's brute-force backoff.
		return "", "", fmt.Errorf("%w: %v", ErrCredentialSecretUnavailable, err)
	}
	if secret.Password == "" {
		return "", "", ErrCredentialInvalid
	}
	return credential.PublicKey, secret.Password, nil
}

// applySecret seals the plaintext into the credential row. The plaintext bytes
// are wiped as soon as the AEAD output exists.
func (s *CredentialService) applySecret(credential *storage.Credential, secret CredentialSecret) error {
	if s == nil || s.secretStore == nil {
		return ErrCredentialSecretUnavailable
	}
	payload, err := json.Marshal(credentialSecretBlob{
		Password: secret.Password, PrivateKey: secret.PrivateKey, Passphrase: secret.Passphrase,
	})
	if err != nil {
		return fmt.Errorf("%w: encode credential secret", ErrCredentialInvalid)
	}
	ciphertext, nonce, keyID, version, err := s.secretStore.Encrypt(string(payload))
	clear(payload)
	if err != nil {
		return err
	}
	credential.SecretCiphertext = base64.RawStdEncoding.EncodeToString(ciphertext)
	credential.SecretNonce = base64.RawStdEncoding.EncodeToString(nonce)
	credential.SecretKeyID = keyID
	credential.SecretVersion = version
	return nil
}

func (s *CredentialService) openSecret(credential storage.Credential) (*CredentialSecret, error) {
	if s == nil || s.secretStore == nil {
		return nil, ErrCredentialSecretUnavailable
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(credential.SecretCiphertext)
	if err != nil {
		return nil, errors.New("stored credential secret is invalid")
	}
	nonce, err := base64.RawStdEncoding.DecodeString(credential.SecretNonce)
	if err != nil {
		return nil, errors.New("stored credential secret nonce is invalid")
	}
	plaintext, err := s.secretStore.Decrypt(ciphertext, nonce, credential.SecretKeyID, credential.SecretVersion)
	if err != nil {
		return nil, err
	}
	payload := []byte(plaintext)
	defer clear(payload)
	var blob credentialSecretBlob
	if err := json.Unmarshal(payload, &blob); err != nil {
		return nil, errors.New("stored credential secret payload is invalid")
	}
	return &CredentialSecret{Password: blob.Password, PrivateKey: blob.PrivateKey, Passphrase: blob.Passphrase}, nil
}

// validateCredentialShape checks the effective credential state (stored fields
// plus the incoming secret) before it reaches the repository, so callers get a
// 400 instead of a storage-level constraint error.
func validateCredentialShape(credential storage.Credential, secret *CredentialSecret, hasStoredSecret bool) error {
	if strings.TrimSpace(credential.Name) == "" {
		return fmt.Errorf("%w: name is required", ErrCredentialInvalid)
	}
	switch credential.Type {
	case storage.CredentialTypeSSHPublicKey:
		if strings.TrimSpace(credential.PublicKey) == "" || strings.TrimSpace(credential.Fingerprint) == "" {
			return fmt.Errorf("%w: public key is required", ErrCredentialInvalid)
		}
	case storage.CredentialTypeProxyBasic:
		if strings.TrimSpace(credential.PublicKey) == "" || strings.TrimSpace(credential.Fingerprint) == "" {
			return fmt.Errorf("%w: a proxy credential requires a username", ErrCredentialInvalid)
		}
	case storage.CredentialTypePassword:
	default:
		return fmt.Errorf("%w: unsupported credential type %q", ErrCredentialInvalid, credential.Type)
	}
	if secret == nil || secret.isEmpty() {
		switch credential.Type {
		case storage.CredentialTypePassword, storage.CredentialTypeProxyBasic:
			// Both kinds are useless without a secret: WebSSH cannot
			// auto-authenticate and the proxy entry has nothing to compare a
			// Basic header against.
			if !hasStoredSecret {
				return fmt.Errorf("%w: a %s credential requires a password", ErrCredentialInvalid, credential.Type)
			}
		}
		return nil
	}
	if len(secret.Password) > maxCredentialPasswordBytes {
		return fmt.Errorf("%w: password exceeds 4 KiB", ErrCredentialInvalid)
	}
	if len(secret.PrivateKey) > maxSSHPrivateKeyBytes {
		return fmt.Errorf("%w: private key exceeds 64 KiB", ErrCredentialInvalid)
	}
	if len(secret.Passphrase) > maxCredentialPassphraseBytes {
		return fmt.Errorf("%w: passphrase exceeds 1 KiB", ErrCredentialInvalid)
	}
	switch credential.Type {
	case storage.CredentialTypePassword:
		if secret.Password == "" {
			return fmt.Errorf("%w: a password credential requires a password", ErrCredentialInvalid)
		}
		if secret.PrivateKey != "" || secret.Passphrase != "" {
			return fmt.Errorf("%w: a password credential must not carry a private key", ErrCredentialInvalid)
		}
	case storage.CredentialTypeProxyBasic:
		if secret.Password == "" {
			return fmt.Errorf("%w: a proxy credential requires a password", ErrCredentialInvalid)
		}
		if secret.PrivateKey != "" || secret.Passphrase != "" {
			return fmt.Errorf("%w: a proxy credential must not carry SSH key material", ErrCredentialInvalid)
		}
	default:
		if secret.Password != "" {
			return fmt.Errorf("%w: a public-key credential stores a private key instead of a password", ErrCredentialInvalid)
		}
		if secret.PrivateKey == "" {
			return fmt.Errorf("%w: a passphrase requires a private key", ErrCredentialInvalid)
		}
	}
	return nil
}

func mapCredentialStorageError(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if strings.Contains(message, "credential owner and name are required") ||
		strings.Contains(message, "credential public key and fingerprint are required") ||
		strings.Contains(message, "credential username and fingerprint are required") ||
		strings.Contains(message, "proxy credential requires an encrypted password") ||
		strings.Contains(message, "unsupported credential type") {
		return fmt.Errorf("%w: %s", ErrCredentialInvalid, message)
	}
	return err
}

func (s *CredentialService) Delete(ctx context.Context, actor auth.Principal, id string) error {
	if _, err := s.Get(ctx, actor, id); err != nil {
		return err
	}
	if err := s.credentials.Delete(ctx, id, time.Now().UTC()); err != nil {
		return err
	}
	s.writeAudit(ctx, actor, "credential.deleted", id)
	return nil
}

func (s *CredentialService) Restore(ctx context.Context, actor auth.Principal, id string) (storage.Credential, error) {
	if _, err := s.Get(ctx, actor, id); err != nil {
		return storage.Credential{}, err
	}
	if err := s.credentials.Restore(ctx, id); err != nil {
		return storage.Credential{}, err
	}
	restored, err := s.Get(ctx, actor, id)
	if err == nil {
		s.writeAudit(ctx, actor, "credential.restored", id)
	}
	return restored, err
}

func (s *CredentialService) List(ctx context.Context, actor auth.Principal, filter CredentialListFilter, cursor string, limit int) (storage.Page[storage.Credential], error) {
	if !isAdmin(actor) {
		filter.OwnerUserID = actor.UserID
	}
	return s.credentials.List(ctx, filter, cursor, limit)
}

// ExtractSSHPublicKey parses an uploaded private key in memory and returns only
// its public-key representation. The private key and passphrase are never
// persisted, logged, or relayed; callers must treat this as a one-time parse.
func (s *CredentialService) ExtractSSHPublicKey(_ context.Context, _ auth.Principal, privateKey, passphrase string) (ExtractedSSHPublicKey, error) {
	keyBytes := []byte(strings.TrimSpace(privateKey))
	defer clear(keyBytes)
	passphraseBytes := []byte(passphrase)
	defer clear(passphraseBytes)
	if len(keyBytes) == 0 {
		return ExtractedSSHPublicKey{}, fmt.Errorf("%w: private key is required", ErrCredentialInvalid)
	}
	if len(keyBytes) > maxSSHPrivateKeyBytes {
		return ExtractedSSHPublicKey{}, fmt.Errorf("%w: private key exceeds 64 KiB", ErrCredentialInvalid)
	}

	var signer ssh.Signer
	var err error
	if len(passphraseBytes) > 0 {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(keyBytes, passphraseBytes)
	} else {
		signer, err = ssh.ParsePrivateKey(keyBytes)
	}
	if err != nil {
		return ExtractedSSHPublicKey{}, fmt.Errorf("%w: private key could not be parsed", ErrCredentialInvalid)
	}
	normalized, fingerprint, err := ParseOpenSSHPublicKey(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	if err != nil {
		return ExtractedSSHPublicKey{}, err
	}
	return ExtractedSSHPublicKey{PublicKey: normalized, Fingerprint: fingerprint}, nil
}

func (s *CredentialService) writeAudit(ctx context.Context, actor auth.Principal, action, id string) {
	if s == nil || s.audits == nil {
		return
	}
	_ = s.audits.Create(ctx, storage.AuditLog{ActorUserID: actor.UserID, Action: action, ResourceType: "credential", ResourceID: id, Details: "{}"})
}

// ParseOpenSSHPublicKey validates the untrusted public-key wire format and
// derives a stable SHA256 fingerprint without accepting private-key material.
func ParseOpenSSHPublicKey(raw string) (string, string, error) {
	parts := strings.Fields(strings.TrimSpace(raw))
	if len(parts) < 2 {
		return "", "", fmt.Errorf("%w: public key is required", ErrCredentialInvalid)
	}
	algorithm, encoded := parts[0], parts[1]
	switch algorithm {
	case "ssh-ed25519", "ssh-rsa", "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521":
	default:
		return "", "", fmt.Errorf("%w: unsupported algorithm %q", ErrCredentialInvalid, algorithm)
	}
	payload, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", "", fmt.Errorf("%w: public key body must be base64", ErrCredentialInvalid)
	}
	algorithmName, rest, err := parseSSHString(payload)
	if err != nil || string(algorithmName) != algorithm {
		return "", "", fmt.Errorf("%w: public key algorithm does not match body", ErrCredentialInvalid)
	}
	if len(rest) == 0 {
		return "", "", fmt.Errorf("%w: public key body is incomplete", ErrCredentialInvalid)
	}
	sum := sha256.Sum256([]byte(algorithm + " " + encoded))
	return algorithm + " " + encoded, "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]), nil
}

// proxyBasicIdentity normalizes a proxy username and derives a stable
// fingerprint for it.
//
// The username is not a secret — it travels in cleartext inside every Basic
// header — so it is stored in the public_key column and only the password is
// sealed. A colon is rejected because RFC 7617 forbids it in the userid and the
// entry splits the decoded header on the first colon; accepting one here would
// create a credential that can never authenticate.
func proxyBasicIdentity(username string) (string, string, error) {
	normalized := strings.TrimSpace(username)
	if normalized == "" {
		return "", "", fmt.Errorf("%w: a proxy credential requires a username", ErrCredentialInvalid)
	}
	if len(normalized) > maxCredentialUsernameBytes {
		return "", "", fmt.Errorf("%w: username exceeds %d bytes", ErrCredentialInvalid, maxCredentialUsernameBytes)
	}
	for _, r := range normalized {
		if r == ':' || r < ' ' || r == 0x7f {
			return "", "", fmt.Errorf("%w: username must not contain a colon or control character", ErrCredentialInvalid)
		}
	}
	sum := sha256.Sum256([]byte(normalized))
	return normalized, hex.EncodeToString(sum[:]), nil
}

func parseSSHString(payload []byte) ([]byte, []byte, error) {
	if len(payload) < 4 {
		return nil, nil, errors.New("ssh wire string is too short")
	}
	size := int(payload[0])<<24 | int(payload[1])<<16 | int(payload[2])<<8 | int(payload[3])
	if size < 0 || len(payload) < 4+size {
		return nil, nil, errors.New("ssh wire string is truncated")
	}
	return payload[4 : 4+size], payload[4+size:], nil
}

func hostIsIPv4(host string) bool {
	return net.ParseIP(host) != nil
}
