package auth

import (
	"crypto/sha256"
	"regexp"
	"strings"
	"testing"
)

var recoveryCodePattern = regexp.MustCompile(`^tmrc-[A-Z2-7]{20}$`)

func TestGenerateRecoveryCodesShapeAndUniqueness(t *testing.T) {
	codes, hashes, err := GenerateRecoveryCodes(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != 10 || len(hashes) != 10 {
		t.Fatalf("generated %d codes and %d hashes", len(codes), len(hashes))
	}
	seen := map[string]bool{}
	for i, code := range codes {
		if !recoveryCodePattern.MatchString(code) {
			t.Fatalf("code %q does not match the documented shape", code)
		}
		if seen[code] {
			t.Fatalf("duplicate code %q", code)
		}
		seen[code] = true
		if len(hashes[i]) != sha256.Size {
			t.Fatalf("hash %d is %d bytes, want %d", i, len(hashes[i]), sha256.Size)
		}
		if strings.Contains(hashes[i], strings.TrimPrefix(code, "tmrc-")) {
			t.Fatalf("hash %d leaks the plaintext code", i)
		}
		if !RecoveryCodeMatches([]byte(hashes[i]), code) {
			t.Fatalf("code %q does not match its own hash", code)
		}
	}
}

func TestGenerateRecoveryCodesRejectsBadCount(t *testing.T) {
	for _, count := range []int{0, -1} {
		if _, _, err := GenerateRecoveryCodes(count); err == nil {
			t.Fatalf("count %d accepted", count)
		}
	}
	one, hashes, err := GenerateRecoveryCodes(1)
	if err != nil || len(one) != 1 || len(hashes) != 1 {
		t.Fatalf("single code = %v, %v, err = %v", one, hashes, err)
	}
}

func TestRecoveryCodeMatchesIsCaseAndSpaceInsensitive(t *testing.T) {
	codes, hashes, err := GenerateRecoveryCodes(1)
	if err != nil {
		t.Fatal(err)
	}
	stored := []byte(hashes[0])
	code := codes[0]
	body := strings.TrimPrefix(code, "tmrc-")
	for _, candidate := range []string{
		code,
		strings.ToLower(code),
		"  " + code + "  ",
		"tmrc-" + body[:4] + " " + body[4:8] + " " + body[8:],
		"TMRC-" + body,
	} {
		if !RecoveryCodeMatches(stored, candidate) {
			t.Fatalf("candidate %q did not match", candidate)
		}
	}
}

func TestRecoveryCodeMatchesRejectsWrongInput(t *testing.T) {
	_, hashes, err := GenerateRecoveryCodes(2)
	if err != nil {
		t.Fatal(err)
	}
	stored := []byte(hashes[0])
	for _, candidate := range []string{"", "tmrc-", "tmrc-AAAAAAAAAAAAAAAAAAAA", hashes[1], "123456"} {
		if RecoveryCodeMatches(stored, candidate) {
			t.Fatalf("candidate %q unexpectedly matched", candidate)
		}
	}
	if RecoveryCodeMatches(nil, "tmrc-AAAAAAAAAAAAAAAAAAAA") {
		t.Fatal("nil hash matched")
	}
	if RecoveryCodeMatches([]byte("short"), "tmrc-AAAAAAAAAAAAAAAAAAAA") {
		t.Fatal("short hash matched")
	}
	other, otherHashes, err := GenerateRecoveryCodes(1)
	if err != nil {
		t.Fatal(err)
	}
	if RecoveryCodeMatches([]byte(otherHashes[0]), other[0]+"X") {
		t.Fatal("mutated code matched")
	}
}

func TestIsRecoveryCodeDistinguishesFromTOTP(t *testing.T) {
	for _, candidate := range []string{"tmrc-AAAAAAAAAAAAAAAAAAAA", "TMRC-AAAAAAAAAAAAAAAAAAAA", "  tmrc-AAAAAAAAAAAAAAAAAAAA"} {
		if !IsRecoveryCode(candidate) {
			t.Fatalf("%q not detected as a recovery code", candidate)
		}
	}
	for _, candidate := range []string{"123456", "12345678", "", "tmr-", "abc"} {
		if IsRecoveryCode(candidate) {
			t.Fatalf("%q wrongly detected as a recovery code", candidate)
		}
	}
	if RecoveryCodePrefix() != "tmrc-" {
		t.Fatalf("prefix = %q", RecoveryCodePrefix())
	}
}

func TestHashRecoveryCodeIsDeterministic(t *testing.T) {
	first := HashRecoveryCode("tmrc-AAAAAAAAAAAAAAAAAAAA")
	second := HashRecoveryCode("TMRC-AAAAAAAAAAAAAAAAAAAA")
	if len(first) != sha256.Size {
		t.Fatalf("hash length = %d", len(first))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatal("hash is not case-insensitive")
		}
	}
	third := HashRecoveryCode("tmrc-BBBBBBBBBBBBBBBBBBBB")
	if string(first) == string(third) {
		t.Fatal("distinct codes produced the same hash")
	}
}
