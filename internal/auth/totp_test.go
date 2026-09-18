package auth

import (
	"encoding/base32"
	"net/url"
	"strings"
	"testing"
	"time"
)

// rfc6238Secret is the ASCII seed "12345678901234567890" from the RFC 6238
// appendix, base32 encoded the way an authenticator app would store it.
const rfc6238Secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

func rfc6238Vectors(t *testing.T) []struct {
	when     int64
	eightDig string
} {
	t.Helper()
	return []struct {
		when     int64
		eightDig string
	}{
		{59, "94287082"},
		{1111111109, "07081804"},
		{1111111111, "14050471"},
		{1234567890, "89005924"},
		{2000000000, "69279037"},
		{20000000000, "65353130"},
	}
}

func TestTOTPMatchesRFC6238Vectors(t *testing.T) {
	for _, vector := range rfc6238Vectors(t) {
		at := time.Unix(vector.when, 0).UTC()
		eight, err := TOTPCode(rfc6238Secret, at, TOTPParams{Digits: 8, Period: 30 * time.Second, Skew: 1})
		if err != nil {
			t.Fatalf("T=%d: %v", vector.when, err)
		}
		if eight != vector.eightDig {
			t.Fatalf("T=%d: 8-digit code = %s, want %s", vector.when, eight, vector.eightDig)
		}
		six, err := TOTPCode(rfc6238Secret, at, TOTPParams{Digits: 6, Period: 30 * time.Second, Skew: 1})
		if err != nil {
			t.Fatalf("T=%d: %v", vector.when, err)
		}
		if want := vector.eightDig[2:]; six != want {
			t.Fatalf("T=%d: 6-digit code = %s, want %s", vector.when, six, want)
		}
	}
}

func TestVerifyTOTPAcceptsCurrentAndSkewSteps(t *testing.T) {
	params := TOTPParams{Digits: 8, Period: 30 * time.Second, Skew: 1}
	now := time.Unix(1234567890, 0).UTC()
	for _, offset := range []time.Duration{-30 * time.Second, 0, 30 * time.Second} {
		code, err := TOTPCode(rfc6238Secret, now.Add(offset), params)
		if err != nil {
			t.Fatal(err)
		}
		step, ok, err := VerifyTOTP(rfc6238Secret, code, now, params)
		if err != nil || !ok {
			t.Fatalf("offset %v: ok = %v, err = %v", offset, ok, err)
		}
		if want := now.Add(offset).Unix() / 30; step != want {
			t.Fatalf("offset %v: step = %d, want %d", offset, step, want)
		}
	}
	// Two steps away is outside the skew window.
	far, err := TOTPCode(rfc6238Secret, now.Add(60*time.Second), params)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := VerifyTOTP(rfc6238Secret, far, now, params); err != nil || ok {
		t.Fatalf("far step ok = %v, err = %v, want false", ok, err)
	}
}

func TestVerifyTOTPSkewZeroAcceptsOnlyCurrentStep(t *testing.T) {
	params := TOTPParams{Digits: 6, Period: 30 * time.Second, Skew: 0}
	now := time.Unix(2000000000, 0).UTC()
	previous, err := TOTPCode(rfc6238Secret, now.Add(-30*time.Second), params)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := VerifyTOTP(rfc6238Secret, previous, now, params); err != nil || ok {
		t.Fatalf("previous step accepted with skew 0: ok = %v, err = %v", ok, err)
	}
	current, err := TOTPCode(rfc6238Secret, now, params)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := VerifyTOTP(rfc6238Secret, current, now, params); err != nil || !ok {
		t.Fatalf("current step rejected with skew 0: ok = %v, err = %v", ok, err)
	}
}

func TestVerifyTOTPRejectsMalformedCodes(t *testing.T) {
	params := DefaultTOTPParams()
	now := time.Unix(1234567890, 0).UTC()
	for _, code := range []string{"", " ", "12345", "1234567", "abcdef", "1234567890", "-12345"} {
		step, ok, err := VerifyTOTP(rfc6238Secret, code, now, params)
		if err != nil {
			t.Fatalf("code %q returned err %v, want a clean rejection", code, err)
		}
		if ok || step != 0 {
			t.Fatalf("code %q was accepted with step %d", code, step)
		}
	}
	// Whitespace around a valid code is tolerated because authenticator apps
	// often paste with a separating space.
	valid, err := TOTPCode(rfc6238Secret, now, params)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := VerifyTOTP(rfc6238Secret, " "+valid+" ", now, params); err != nil || !ok {
		t.Fatalf("padded valid code rejected: ok = %v, err = %v", ok, err)
	}
}

func TestTOTPRejectsInvalidParamsAndSecret(t *testing.T) {
	now := time.Unix(1234567890, 0).UTC()
	for _, params := range []TOTPParams{
		{Digits: 7, Period: 30 * time.Second, Skew: 1},
		{Digits: 0, Period: 30 * time.Second, Skew: 1},
		{Digits: 6, Period: 5 * time.Second, Skew: 1},
		{Digits: 6, Period: 300 * time.Second, Skew: 1},
		{Digits: 6, Period: 30 * time.Second, Skew: 3},
		{Digits: 6, Period: 30 * time.Second, Skew: -1},
	} {
		if _, err := TOTPCode(rfc6238Secret, now, params); err == nil {
			t.Fatalf("params %+v accepted", params)
		}
		if _, _, err := VerifyTOTP(rfc6238Secret, "123456", now, params); err == nil {
			t.Fatalf("verify with params %+v accepted", params)
		}
	}
	for _, secret := range []string{"", "not-base32!!!", "===="} {
		if _, err := TOTPCode(secret, now, DefaultTOTPParams()); err == nil {
			t.Fatalf("secret %q accepted", secret)
		}
		if _, _, err := VerifyTOTP(secret, "123456", now, DefaultTOTPParams()); err == nil {
			t.Fatalf("verify with secret %q accepted", secret)
		}
	}
}

func TestGenerateTOTPSecretIsDecodableAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		secret, err := GenerateTOTPSecret()
		if err != nil {
			t.Fatal(err)
		}
		if seen[secret] {
			t.Fatalf("duplicate secret generated: %s", secret)
		}
		seen[secret] = true
		decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
		if err != nil {
			t.Fatalf("secret %q is not base32: %v", secret, err)
		}
		if len(decoded) != totpSecretBytes {
			t.Fatalf("secret decoded to %d bytes, want %d", len(decoded), totpSecretBytes)
		}
		if _, err := TOTPCode(secret, time.Unix(1234567890, 0), DefaultTOTPParams()); err != nil {
			t.Fatalf("generated secret unusable: %v", err)
		}
	}
}

func TestDecodeTOTPSecretAcceptsGroupedAndPaddedForms(t *testing.T) {
	first, err := TOTPCode(rfc6238Secret, time.Unix(1234567890, 0), DefaultTOTPParams())
	if err != nil {
		t.Fatal(err)
	}
	grouped := strings.ToLower(rfc6238Secret[:4] + " " + rfc6238Secret[4:8] + " " + rfc6238Secret[8:])
	second, err := TOTPCode(grouped, time.Unix(1234567890, 0), DefaultTOTPParams())
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("grouped secret produced %s, want %s", second, first)
	}
}

func TestOTPAuthURLFormat(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := OTPAuthURL("TunnelMesh", "alice@corp.example.com", secret, DefaultTOTPParams())
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("otpauth url %q does not parse: %v", raw, err)
	}
	if parsed.Scheme != "otpauth" || parsed.Host != "totp" {
		t.Fatalf("scheme/host = %s/%s", parsed.Scheme, parsed.Host)
	}
	if want := "TunnelMesh:alice@corp.example.com"; parsed.Path != "/"+want {
		t.Fatalf("path = %q, want %q", parsed.Path, "/"+want)
	}
	query := parsed.Query()
	if query.Get("secret") != strings.ToUpper(secret) {
		t.Fatalf("secret = %q", query.Get("secret"))
	}
	if query.Get("issuer") != "TunnelMesh" || query.Get("algorithm") != "SHA1" || query.Get("digits") != "6" || query.Get("period") != "30" {
		t.Fatalf("query = %v", query)
	}
	for _, invalid := range []struct{ issuer, account string }{
		{"", "alice"},
		{strings.Repeat("a", 65), "alice"},
		{"Tunnel:Mesh", "alice"},
		{"TunnelMesh", ""},
		{"TunnelMesh", "   "},
	} {
		if _, err := OTPAuthURL(invalid.issuer, invalid.account, secret, DefaultTOTPParams()); err == nil {
			t.Fatalf("issuer %q account %q accepted", invalid.issuer, invalid.account)
		}
	}
	if _, err := OTPAuthURL("TunnelMesh", "alice", "!!!", DefaultTOTPParams()); err == nil {
		t.Fatal("invalid secret accepted")
	}
	if _, err := OTPAuthURL("TunnelMesh", "alice", secret, TOTPParams{Digits: 5, Period: 30 * time.Second}); err == nil {
		t.Fatal("invalid params accepted")
	}
}

func TestDefaultTOTPParams(t *testing.T) {
	params := DefaultTOTPParams()
	if params.Digits != 6 || params.Period != 30*time.Second || params.Skew != 1 {
		t.Fatalf("defaults = %+v", params)
	}
	if _, err := params.validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
}
