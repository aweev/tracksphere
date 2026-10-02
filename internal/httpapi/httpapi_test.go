package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func sign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifySignature_Valid(t *testing.T) {
	body := `{"trackingNumber":"TS-1","eventId":"e1","code":"DEPARTED"}`
	if !verifySignature([]byte("s3cret"), []byte(body), sign("s3cret", body)) {
		t.Fatal("valid signature rejected")
	}
}

func TestVerifySignature_WrongSecret(t *testing.T) {
	body := `{"a":1}`
	if verifySignature([]byte("s3cret"), []byte(body), sign("other", body)) {
		t.Fatal("wrong-secret signature accepted")
	}
}

func TestVerifySignature_TamperedBody(t *testing.T) {
	sig := sign("s3cret", `{"a":1}`)
	if verifySignature([]byte("s3cret"), []byte(`{"a":2}`), sig) {
		t.Fatal("tampered body accepted")
	}
}

func TestVerifySignature_GarbageHeader(t *testing.T) {
	if verifySignature([]byte("s3cret"), []byte(`{}`), "not-hex!!") {
		t.Fatal("garbage header accepted")
	}
	if verifySignature([]byte("s3cret"), []byte(`{}`), "") {
		t.Fatal("empty header accepted")
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Acme Logistics": "acme-logistics",
		"  Foo   Bar  ":  "foo-bar",
		"???":            "org",
		"Ünicode Org":    "nicode-org",
		"Org_2026":       "org-2026",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestItoa(t *testing.T) {
	if itoa(0) != "0" || itoa(7) != "7" || itoa(42) != "42" {
		t.Fatalf("itoa broken: %s %s %s", itoa(0), itoa(7), itoa(42))
	}
}