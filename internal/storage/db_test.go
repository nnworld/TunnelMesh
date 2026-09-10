package storage

import "testing"

func TestNormalizeMySQLDSNTLSDisablesStaleTLSFlag(t *testing.T) {
	got, err := normalizeMySQLDSNTLS("user:pass@tcp(db:3306)/tunnelmesh?parseTime=true&tls=true", false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "user:pass@tcp(db:3306)/tunnelmesh?parseTime=true&tls=false" {
		t.Fatalf("normalized DSN = %q", got)
	}
}

func TestNormalizeMySQLDSNTLSAddsTLSWhenEnabled(t *testing.T) {
	got, err := normalizeMySQLDSNTLS("user:pass@tcp(db:3306)/tunnelmesh?parseTime=true", true)
	if err != nil {
		t.Fatal(err)
	}
	if got != "user:pass@tcp(db:3306)/tunnelmesh?parseTime=true&tls=true" {
		t.Fatalf("normalized DSN = %q", got)
	}
}

func TestNormalizeMySQLDSNTLSOverridesExplicitDisableWhenEnabled(t *testing.T) {
	got, err := normalizeMySQLDSNTLS("user:pass@tcp(db:3306)/tunnelmesh?tls=false", true)
	if err != nil {
		t.Fatal(err)
	}
	if got != "user:pass@tcp(db:3306)/tunnelmesh?tls=true" {
		t.Fatalf("normalized DSN = %q", got)
	}
}

func TestNormalizeMySQLDSNTLSPreservesCustomTLSProfile(t *testing.T) {
	got, err := normalizeMySQLDSNTLS("user:pass@tcp(db:3306)/tunnelmesh?tls=my-profile", true)
	if err != nil {
		t.Fatal(err)
	}
	if got != "user:pass@tcp(db:3306)/tunnelmesh?tls=my-profile" {
		t.Fatalf("normalized DSN = %q", got)
	}
}
