package auth

import (
	"testing"
	"time"
)

// hotpTest computes the expected OTP for a known vector so VerifyTOTP's core
// math is pinned against RFC 4226 Appendix D style expectations.
func TestHOTPKnownVector(t *testing.T) {
	// RFC 4226 test vector: secret "12345678901234567890", counter 0 → 755224.
	key := []byte("12345678901234567890")
	got := hotp(key, 0)
	if got != "755224" {
		t.Fatalf("HOTP(0) = %s, want 755224", got)
	}
	if hotp(key, 1) != "287082" {
		t.Fatalf("HOTP(1) mismatch: %s", hotp(key, 1))
	}
}

func TestVerifyTOTPRejectsGarbage(t *testing.T) {
	secret, err := NewTOTPSecret()
	if err != nil {
		t.Fatalf("NewTOTPSecret: %v", err)
	}
	if VerifyTOTP(secret, "abcdef") {
		t.Fatal("non-numeric code accepted")
	}
	if VerifyTOTP(secret, "12345") { // wrong length
		t.Fatal("5-digit code accepted")
	}
	if VerifyTOTP("not-base3!!!", "123456") {
		t.Fatal("invalid secret accepted")
	}
}

func TestProvisioningURI(t *testing.T) {
	uri := TOTPProvisioningURI("JBSWY3DPEHPK3PXP", "user@example.com")
	if uri == "" || uri[:15] != "otpauth://totp/" {
		t.Fatalf("bad uri: %s", uri)
	}
	for _, want := range []string{"secret=JBSWY3DPEHPK3PXP", "issuer=TrackSphere", "period=30"} {
		if !contains(uri, want) {
			t.Fatalf("uri missing %q: %s", want, uri)
		}
	}
}

func TestNewTOTPSecretFormat(t *testing.T) {
	s, err := NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(s) != 32 { // 160 bits base32 → 32 chars unpadded
		t.Fatalf("secret length %d, want 32", len(s))
	}
}

// TestVerifyTOTPRoundTrip validates a code generated from the same secret
// using the current time step (window 0), which must always pass.
func TestVerifyTOTPRoundTrip(t *testing.T) {
	secret, _ := NewTOTPSecret()
	key, err := decodeBase32(secret)
	if err != nil {
		t.Fatal(err)
	}
	counter := uint64(time.Now().Unix() / 30)
	code := hotp(key, counter)
	if !VerifyTOTP(secret, code) {
		t.Fatal("freshly generated code rejected")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) &&
		(func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		})()
}