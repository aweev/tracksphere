package auth

import (
	"strings"
	"testing"
)

func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("expected PHC argon2id prefix, got %q", hash)
	}
	if !VerifyPassword(hash, "correct horse battery staple") {
		t.Fatal("correct password rejected")
	}
	if VerifyPassword(hash, "wrong password") {
		t.Fatal("wrong password accepted")
	}
	// Tampered hash must fail closed.
	if VerifyPassword(hash[:len(hash)-4]+"AAAA", "correct horse battery staple") {
		t.Fatal("tampered hash accepted")
	}
}

func TestPasswordMinLength(t *testing.T) {
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("expected error for password shorter than 8 chars")
	}
}

func TestSessionTokenRoundTrip(t *testing.T) {
	raw, hash, err := NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken: %v", err)
	}
	if len(raw) < 40 {
		t.Fatalf("token too short: %d chars", len(raw))
	}
	if string(HashToken(raw)) != string(hash) {
		t.Fatal("HashToken mismatch")
	}
	other, _, _ := NewSessionToken()
	if other == raw {
		t.Fatal("tokens are not unique")
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	key := []byte("test-secret-key")
	sealed, err := Seal(key, []byte("JBSWY3DPEHPK3PXP"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	plain, err := Open(key, sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if string(plain) != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("round trip mismatch: %q", plain)
	}
	if _, err := Open([]byte("wrong-key"), sealed); err == nil {
		t.Fatal("wrong key opened sealed data")
	}
	// Two seals of the same plaintext must differ (fresh nonce).
	sealed2, _ := Seal(key, []byte("JBSWY3DPEHPK3PXP"))
	if sealed == sealed2 {
		t.Fatal("nonce reused across seals")
	}
}