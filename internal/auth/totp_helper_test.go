package auth

import "encoding/base32"

// decodeBase32 mirrors the decoding used by VerifyTOTP (test seam).
func decodeBase32(s string) ([]byte, error) {
	return base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(s)
}