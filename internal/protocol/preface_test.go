package protocol

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestPrefaceRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WritePreface(&buf, []byte("tm_agent_secret")); err != nil {
		t.Fatalf("write: %v", err)
	}
	token, err := ReadPreface(&buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(token) != "tm_agent_secret" {
		t.Fatalf("token = %q", token)
	}
}

func TestPrefaceRejectsBadMagicVersionAndFlags(t *testing.T) {
	cases := map[string][]byte{
		"bad magic":   []byte("XXXX\x01\x00\x00\x03abc"),
		"bad version": []byte("TMAG\x02\x00\x00\x03abc"),
		"bad flags":   []byte("TMAG\x01\x01\x00\x03abc"),
	}
	for name, raw := range cases {
		if _, err := ReadPreface(bytes.NewReader(raw)); !errors.Is(err, ErrBadPreface) {
			t.Errorf("%s: err = %v, want ErrBadPreface", name, err)
		}
	}
}

func TestPrefaceRejectsEmptyAndOversizedTokens(t *testing.T) {
	if err := WritePreface(&bytes.Buffer{}, nil); !errors.Is(err, ErrBadPreface) {
		t.Fatalf("empty token: err = %v", err)
	}
	long := strings.Repeat("a", MaxPrefaceToken+1)
	if err := WritePreface(&bytes.Buffer{}, []byte(long)); !errors.Is(err, ErrBadPreface) {
		t.Fatalf("oversized token on write: err = %v", err)
	}
	header := []byte{'T', 'M', 'A', 'G', 1, 0}
	header = append(header, byte((MaxPrefaceToken+1)>>8), byte((MaxPrefaceToken+1)&0xff))
	if _, err := ReadPreface(bytes.NewReader(header)); !errors.Is(err, ErrBadPreface) {
		t.Fatalf("declared oversize must be refused before reading the body: err = %v", err)
	}
	zero := []byte{'T', 'M', 'A', 'G', 1, 0, 0, 0}
	if _, err := ReadPreface(bytes.NewReader(zero)); !errors.Is(err, ErrBadPreface) {
		t.Fatalf("zero-length token: err = %v", err)
	}
}

func TestPrefaceErrorsNeverEchoTheToken(t *testing.T) {
	raw := []byte("TMAG\x09\x00\x00\x06secret")
	_, err := ReadPreface(bytes.NewReader(raw))
	if err == nil {
		t.Fatal("unsupported version must fail")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("error leaks token bytes: %q", err)
	}
}

func TestPrefaceTokenRedactsInLogs(t *testing.T) {
	token := PrefaceToken([]byte("tm_agent_secret"))
	for _, rendered := range []string{token.String(), token.GoString(), token.LogValue()} {
		if strings.Contains(rendered, "tm_agent_secret") || rendered != "[redacted]" {
			t.Fatalf("rendered %q", rendered)
		}
	}
}

func TestPrefaceStatusCodes(t *testing.T) {
	for status := StatusOK; status <= StatusBusy; status++ {
		if status.String() == "" || strings.HasPrefix(status.String(), "status(") {
			t.Fatalf("status %d has no name", status)
		}
	}
	if PrefaceStatus(200).String() != "status(200)" {
		t.Fatalf("unknown status must render generically")
	}
}

// FuzzReadPreface checks that no byte sequence panics or yields a token longer than
// the limit. The preface is read before authentication, so it is untrusted input.
func FuzzReadPreface(f *testing.F) {
	f.Add([]byte("TMAG\x01\x00\x00\x03abc"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, raw []byte) {
		token, err := ReadPreface(bytes.NewReader(raw))
		if err == nil && (len(token) == 0 || len(token) > MaxPrefaceToken) {
			t.Fatalf("accepted token of length %d", len(token))
		}
	})
}
