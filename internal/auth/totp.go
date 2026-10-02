package auth

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

// ── TOTP (RFC 6238, SHA-1, 6 digits, 30s step — Google Authenticator compat) ──

const (
	totpStep    = 30
	totpDigits  = 6
	totpWindow  = 1 // accept previous/next step to absorb clock drift
	issuerName  = "TrackSphere"
)

// NewTOTPSecret returns a fresh base32-encoded TOTP secret (no padding).
func NewTOTPSecret() (string, error) {
	buf := make([]byte, 20) // 160 bits per RFC 4226 recommendation
	if _, err := randomRead(buf); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf), nil
}

// TOTPProvisioningURI builds the otpauth:// URI accepted by Google
// Authenticator, Authy, 1Password, etc.
func TOTPProvisioningURI(secret, account string) string {
	label := issuerName + ":" + account
	return fmt.Sprintf(
		"otpauth://totp/%s?secret=%s&issuer=%s&digits=%d&period=%d",
		strings.ReplaceAll(label, " ", "%20"),
		secret,
		issuerName,
		totpDigits,
		totpStep,
	)
}

// VerifyTOTP validates code against secret. It tolerates ±1 time step.
func VerifyTOTP(secret, code string) bool {
	code = strings.TrimSpace(strings.ReplaceAll(code, " ", ""))
	if len(code) != totpDigits {
		return false
	}
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil {
		return false
	}
	now := time.Now().Unix()
	counter := now / totpStep
	for _, delta := range []int64{-totpWindow, 0, totpWindow} {
		want := hotp(key, uint64(counter+delta))
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

// hotp computes the 6-digit HOTP value for counter (RFC 4226 §5).
func hotp(key []byte, counter uint64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, bin%pow10(totpDigits))
}

func pow10(n int) uint32 {
	p := uint32(1)
	for i := 0; i < n; i++ {
		p *= 10
	}
	return p
}
