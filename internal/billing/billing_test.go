package billing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"testing"
	"time"
)

func signForTest(t *testing.T, body []byte, secret string) string {
	t.Helper()
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyWebhook(t *testing.T) {
	body := []byte(`{"type":"checkout.session.completed","data":{"object":{"id":"cs_1"}}}`)
	header := signForTest(t, body, "whsec_test")
	ev, err := VerifyWebhook(body, header, "whsec_test")
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != "checkout.session.completed" {
		t.Fatalf("wrong type: %s", ev.Type)
	}
}

func TestVerifyWebhookRejects(t *testing.T) {
	body := []byte(`{"type":"x"}`)
	header := signForTest(t, body, "whsec_other")
	if _, err := VerifyWebhook(body, header, "whsec_test"); err == nil {
		t.Fatal("wrong secret must fail")
	}
	if _, err := VerifyWebhook(body, "t=123,v1=deadbeef", "whsec_test"); err == nil {
		t.Fatal("stale timestamp must fail")
	}
}

func TestPriceFor(t *testing.T) {
	t.Setenv("STRIPE_PRICE_GROWTH", "price_g")
	if PriceFor("growth") != "price_g" {
		t.Fatal("growth price not read")
	}
	if PriceFor("starter") != "" {
		t.Fatal("starter has no price")
	}
}
