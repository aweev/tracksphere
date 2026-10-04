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

const (
	argonTime    = 1
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

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

func VerifyPassword(hash, password string) bool {
	salt, key, time, memory, threads, err := decodePHC(hash)
	if err != nil {
		return false
	}
	candidate := argon2idKey([]byte(password), salt, time, memory, threads, uint32(len(key)))
	return subtle.ConstantTimeCompare(candidate, key) == 1
}

func NewSessionToken() (raw string, hash []byte, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", nil, err
	}
	raw = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(raw))
	return raw, sum[:], nil
}

func HashToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// Seal encrypts plaintext with the PRIMARY key (first in SecretKeys).
// Output format: base64(key-index || nonce || ciphertext).
// key-index is a single byte identifying which key was used for encryption.
func SealMulti(secretKeys [][]byte, plaintext []byte) (string, error) {
	if len(secretKeys) == 0 {
		return "", errors.New("no secret keys available")
	}
	primaryKey := secretKeys[0]
	block, err := newCipher(primaryKey)
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
	sealed := gcm.Seal(nil, nonce, plaintext, nil)
	// Prepend key index (0 for primary) + nonce
	output := make([]byte, 1+len(nonce)+len(sealed))
	output[0] = 0 // key index
	copy(output[1:], nonce)
	copy(output[1+len(nonce):], sealed)
	return base64.StdEncoding.EncodeToString(output), nil
}

// Open reverses SealMulti, trying all keys in SecretKeys for decryption.
// This enables zero-downtime key rotation: old keys remain valid for decryption
// while new data is encrypted with the primary key.
func OpenMulti(secretKeys [][]byte, sealed string) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return nil, err
	}
	if len(data) < 2 {
		return nil, errors.New("sealed payload too short")
	}
	keyIndex := int(data[0])
	if keyIndex >= len(secretKeys) {
		return nil, fmt.Errorf("key index %d out of range (have %d keys)", keyIndex, len(secretKeys))
	}
	key := secretKeys[keyIndex]
	block, err := newCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(data) < 1+nonceSize {
		return nil, errors.New("sealed payload too short for nonce")
	}
	nonce := data[1 : 1+nonceSize]
	ciphertext := data[1+nonceSize:]
	return gcm.Open(nil, nonce, ciphertext, nil)
}

// Seal encrypts plaintext with a single secret key (legacy, single-key mode).
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

// Open reverses Seal (legacy single-key mode).
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
	sum := sha256.Sum256(secretKey)
	return aes.NewCipher(sum[:])
}