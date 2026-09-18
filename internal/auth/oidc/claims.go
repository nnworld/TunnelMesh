package oidc

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Claims is the validated id_token content TunnelMesh acts on. Raw keeps the
// full claim set so role mapping can read provider-specific members without this
// package knowing their names in advance.
type Claims struct {
	Subject       string
	Username      string
	Email         string
	EmailVerified bool
	Raw           map[string]any
}

// ResolveIdentity turns validated claims into the TunnelMesh account name and
// role. It is a pure function so the mapping rules can be tested without an IdP.
//
// The username falls back in three stages: the configured claim, then email, then
// a deterministic provider-scoped placeholder. The last stage exists because a
// just-in-time account must be creatable even when an IdP releases only "sub",
// and it must still satisfy the 3-64 character username rule.
func ResolveIdentity(p Provider, c Claims) (username, role string, err error) {
	// Without sub there is no stable identity, and inventing one would let a
	// claim change silently re-provision an account. VerifyIDToken already
	// rejects a missing sub; this repeats the check so the function is safe to
	// call on its own.
	if strings.TrimSpace(c.Subject) == "" {
		return "", "", errors.Join(ErrClaimMissing, errors.New("sub claim missing"))
	}
	username = sanitizeUsernameSegment(normalizeUsername(c.Username))
	if username == "" {
		// An email is sanitized rather than used verbatim because the account rule
		// admits only [A-Za-z0-9._-]; the mapping stays deterministic, so the same
		// address always resolves to the same account.
		username = sanitizeUsernameSegment(normalizeUsername(c.Email))
	}
	if !validUsername(username) {
		username = fallbackUsername(p, c.Subject)
	}
	if !validUsername(username) {
		return "", "", errors.Join(ErrClaimMissing, fmt.Errorf("resolved username %q does not satisfy the 3-64 character rule", username))
	}
	role, err = resolveRole(p, c)
	if err != nil {
		return "", "", err
	}
	return username, role, nil
}

// resolveRole walks the mappings in order and returns the first match. An
// invalid mapping role is a configuration error and is reported even if a later
// mapping would have matched, because silently ignoring it would let an operator
// believe a rule is in force.
func resolveRole(p Provider, c Claims) (string, error) {
	for _, mapping := range p.RoleMappings {
		claim := strings.TrimSpace(mapping.Claim)
		if claim == "" {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(mapping.Role))
		if role != "admin" && role != "user" {
			return "", errors.Join(ErrRoleMappingInvalid, fmt.Errorf("mapping role %q must be admin or user", mapping.Role))
		}
		// An unknown claim is skipped, not fatal: IdPs differ in which group
		// claim they release, and a missing claim simply means "no match".
		value, ok := c.Raw[claim]
		if !ok {
			continue
		}
		if claimMatches(value, mapping.Value) {
			return role, nil
		}
	}
	return p.defaultRoleValue(), nil
}

// claimMatches supports a scalar claim and a string array, the two shapes group
// claims take in practice. A numeric claim is compared through its formatted
// form so a mapping can target a level value.
func claimMatches(actual any, expected string) bool {
	expected = strings.TrimSpace(expected)
	switch value := actual.(type) {
	case string:
		return strings.EqualFold(strings.TrimSpace(value), expected)
	case bool:
		return fmt.Sprint(value) == expected
	case float64:
		return fmt.Sprint(value) == expected || fmt.Sprintf("%d", int64(value)) == expected
	case []any:
		for _, entry := range value {
			switch item := entry.(type) {
			case string:
				if strings.EqualFold(strings.TrimSpace(item), expected) {
					return true
				}
			case float64:
				if fmt.Sprint(item) == expected {
					return true
				}
			}
		}
	}
	return false
}

// normalizeUsername trims and lowercases. TunnelMesh usernames are matched
// case-insensitively, so the account an IdP creates for "Alice" and "alice" must
// be the same row rather than two competing identities.
func normalizeUsername(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// fallbackUsername derives a stable, provider-scoped name from the subject. Only
// the first 8 bytes of the subject hash are used, which keeps the result well
// inside the username length limit while staying collision-resistant for the
// number of accounts one deployment has.
func fallbackUsername(p Provider, subject string) string {
	sum := sha256.Sum256([]byte(subject))
	name := sanitizeUsernameSegment(p.Name)
	if name == "" {
		name = "oidc"
	}
	if len(name) > 24 {
		name = strings.Trim(name[:24], "-._")
	}
	return fmt.Sprintf("%s-%s", name, hex.EncodeToString(sum[:])[:16])
}

// sanitizeUsernameSegment maps every character the account rule forbids onto an
// underscore and bounds the length, so any IdP-released value yields something
// the local user repository will accept. Trimming the separators keeps a
// username from starting or ending with punctuation.
func sanitizeUsernameSegment(value string) string {
	out := make([]rune, 0, len(value))
	for _, r := range strings.ToLower(value) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out = append(out, r)
		case r == '-' || r == '_' || r == '.':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	trimmed := strings.Trim(string(out), "-._")
	if trimmed == "" {
		return ""
	}
	if len(trimmed) > 48 {
		trimmed = strings.Trim(trimmed[:48], "-._")
	}
	return trimmed
}

// validUsername mirrors the account rule used by the local user repository so an
// SSO-provisioned account cannot be created in a shape the console rejects.
func validUsername(username string) bool {
	if len(username) < 3 || len(username) > 64 {
		return false
	}
	for _, r := range username {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}

// ValidUsername exposes the rule so the provider service can validate a
// configured username claim result during a dry-run test.
func ValidUsername(username string) bool { return validUsername(username) }

// stringClaim reads a string claim, tolerating a missing or non-string value.
func stringClaim(raw map[string]any, key string) string {
	value, ok := raw[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

// boolClaim reads a boolean claim. Some IdPs send "true" as a string, and
// treating that as false would silently mark verified addresses unverified.
func boolClaim(raw map[string]any, key string) bool {
	value, ok := raw[key]
	if !ok {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	}
	return false
}
