package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TOTP implements RFC 6238 on top of RFC 4226 HOTP. SHA-1 is retained because
// every mainstream authenticator app negotiates it; the shared secret is 160
// bits of entropy, so the hash choice is a compatibility decision rather than a
// weakening of the credential.
type TOTPParams struct {
	// Digits is the code length, 6 or 8.
	Digits int
	// Period is the time step in seconds.
	Period time.Duration
	// Skew is the number of adjacent steps accepted on either side of the
	// current one, which absorbs client clock drift.
	Skew int
}

// DefaultTOTPParams matches the settings every common authenticator app uses.
func DefaultTOTPParams() TOTPParams { return TOTPParams{Digits: 6, Period: 30 * time.Second, Skew: 1} }

const totpSecretBytes = 20

var (
	errTOTPDigits     = errors.New("totp digits must be 6 or 8")
	errTOTPPeriod     = errors.New("totp period must be between 15s and 120s")
	errTOTPSkew       = errors.New("totp skew must be between 0 and 2")
	errTOTPSecret     = errors.New("totp secret must be valid base32")
	errTOTPIssuer     = errors.New("totp issuer must be 1-64 characters without a colon")
	errTOTPAccountFmt = errors.New("totp account must not be empty")
)

func (p TOTPParams) validate() (TOTPParams, error) {
	if p.Digits != 6 && p.Digits != 8 {
		return p, errTOTPDigits
	}
	if p.Period < 15*time.Second || p.Period > 120*time.Second {
		return p, errTOTPPeriod
	}
	if p.Skew < 0 || p.Skew > 2 {
		return p, errTOTPSkew
	}
	return p, nil
}

// GenerateTOTPSecret returns a base32-encoded 160-bit shared secret.
func GenerateTOTPSecret() (string, error) {
	buffer := make([]byte, totpSecretBytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate totp secret: %w", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buffer), nil
}

func decodeTOTPSecret(secret string) ([]byte, error) {
	normalized := strings.ToUpper(strings.TrimSpace(secret))
	normalized = strings.ReplaceAll(normalized, " ", "")
	// Authenticator apps commonly display grouped secrets; the padding is
	// optional in base32, so add it back before decoding.
	if pad := len(normalized) % 8; pad != 0 {
		normalized += strings.Repeat("=", 8-pad)
	}
	decoded, err := base32.StdEncoding.DecodeString(normalized)
	if err != nil || len(decoded) == 0 {
		return nil, errTOTPSecret
	}
	return decoded, nil
}

// hotp computes one RFC 4226 code for a counter value.
func hotp(secret []byte, counter uint64, digits int) (string, error) {
	var counterBytes [8]byte
	binary.BigEndian.PutUint64(counterBytes[:], counter)
	mac := hmac.New(sha1.New, secret)
	if _, err := mac.Write(counterBytes[:]); err != nil {
		return "", fmt.Errorf("compute hotp: %w", err)
	}
	sum := mac.Sum(nil)
	// Dynamic truncation, RFC 4226 section 5.3.
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	modulus := uint32(1)
	for i := 0; i < digits; i++ {
		modulus *= 10
	}
	code := value % modulus
	return strings.Repeat("0", digits-len(strconv.FormatUint(uint64(code), 10))) + strconv.FormatUint(uint64(code), 10), nil
}

// TOTPCode returns the code that is current at the supplied instant.
func TOTPCode(secret string, at time.Time, params TOTPParams) (string, error) {
	validated, err := params.validate()
	if err != nil {
		return "", err
	}
	decoded, err := decodeTOTPSecret(secret)
	if err != nil {
		return "", err
	}
	return hotp(decoded, uint64(toptStep(at, validated.Period)), validated.Digits)
}

func toptStep(at time.Time, period time.Duration) int64 {
	return at.Unix() / int64(period/time.Second)
}

// VerifyTOTP checks a code against the current step and the configured skew.
// It returns the accepted counter value so the caller can enforce a monotonic
// replay guard: a code may only be consumed once even inside the skew window.
func VerifyTOTP(secret, code string, at time.Time, params TOTPParams) (int64, bool, error) {
	params, err := params.validate()
	if err != nil {
		return 0, false, err
	}
	decoded, err := decodeTOTPSecret(secret)
	if err != nil {
		return 0, false, err
	}
	code = strings.TrimSpace(code)
	if len(code) != params.Digits {
		return 0, false, nil
	}
	if _, err := strconv.Atoi(code); err != nil {
		return 0, false, nil
	}
	current := toptStep(at, params.Period)
	// Scan the current step first: it is overwhelmingly the common case, and a
	// constant-time comparison keeps the search order from leaking which step a
	// submitted code belonged to.
	for _, offset := range skewOrder(params.Skew) {
		step := current + int64(offset)
		if step < 0 {
			continue
		}
		expected, err := hotp(decoded, uint64(step), params.Digits)
		if err != nil {
			return 0, false, err
		}
		if hmac.Equal([]byte(expected), []byte(code)) {
			return step, true, nil
		}
	}
	return 0, false, nil
}

// skewOrder yields 0 first, then alternating negative and positive offsets so
// the nearest steps are tried before the furthest ones.
func skewOrder(skew int) []int {
	order := make([]int, 0, skew*2+1)
	order = append(order, 0)
	for delta := 1; delta <= skew; delta++ {
		order = append(order, -delta, delta)
	}
	return order
}

// OTPAuthURL renders the otpauth:// URI that authenticator apps scan. The
// issuer and account are percent-encoded inside the label so a colon in either
// value cannot change the URI structure.
func OTPAuthURL(issuer, account, secret string, params TOTPParams) (string, error) {
	if _, err := params.validate(); err != nil {
		return "", err
	}
	issuer = strings.TrimSpace(issuer)
	if issuer == "" || len(issuer) > 64 || strings.Contains(issuer, ":") {
		return "", errTOTPIssuer
	}
	account = strings.TrimSpace(account)
	if account == "" {
		return "", errTOTPAccountFmt
	}
	if _, err := decodeTOTPSecret(secret); err != nil {
		return "", err
	}
	query := url.Values{}
	query.Set("secret", strings.ToUpper(strings.TrimSpace(secret)))
	query.Set("issuer", issuer)
	query.Set("algorithm", "SHA1")
	query.Set("digits", strconv.Itoa(params.Digits))
	query.Set("period", strconv.Itoa(int(params.Period/time.Second)))
	return "otpauth://totp/" + url.PathEscape(issuer) + ":" + url.PathEscape(account) + "?" + query.Encode(), nil
}
