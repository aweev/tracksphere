// Package auth implements credential hashing, session token minting, TOTP
// multi-factor authentication and the AES-256-GCM seal used to store TOTP
// secrets at rest.
package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// ── Password hashing (argon2id, PHC string format) ─────────────────────

const (
	argonTime    = 1
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

// HashPassword returns a PHC-encoded argon2id hash: $argon2id$v=19$m=...,t=...,p=...
func HashPassword(password string) (string, error) {
	if len(password) < 8 {
		return "", errors.New("password must be at least 8 characters")
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2idKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword reports whether password matches the stored PHC hash.
// Comparison is constant-time.
func VerifyPassword(hash, password string) bool {
	salt, key, time, memory, threads, err := decodePHC(hash)
	if err != nil {
		return false
	}
	candidate := argon2idKey([]byte(password), salt, time, memory, threads, uint32(len(key)))
	return subtle.ConstantTimeCompare(candidate, key) == 1
}

// ── Session tokens ─────────────────────────────────────────────────────

// NewSessionToken returns a 32-byte random token (base64url) and its
// SHA-256 hash. Only the hash is persisted; the raw token is the cookie value.
func NewSessionToken() (raw string, hash []byte, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", nil, err
	}
	raw = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(raw))
	return raw, sum[:], nil
}

// HashToken returns the SHA-256 hash of a raw token for DB lookup.
func HashToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// ── AES-256-GCM sealing (TOTP secrets at rest) ─────────────────────────

// Seal encrypts plaintext with a key derived from secretKey.
// Output format: base64(nonce || ciphertext).
func Seal(secretKey, plaintext []byte) (string, error) {
	block, err := newCipher(secretKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Open reverses Seal.
func Open(secretKey []byte, sealed string) ([]byte, error) {
	block, err := newCipher(secretKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return nil, err
	}
	if len(data) < gcm.NonceSize() {
		return nil, errors.New("sealed payload too short")
	}
	return gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
}

func newCipher(secretKey []byte) (cipher.Block, error) {
	if len(secretKey) == 0 {
		return nil, errors.New("empty secret key")
	}
	// Derive a stable 32-byte key so operators may pass any passphrase.
	sum := sha256.Sum256(secretKey)
	return aes.NewCipher(sum[:])
}
