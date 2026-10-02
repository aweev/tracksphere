package auth

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2idKey derives a key with the given parameters.
func argon2idKey(password, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte {
	return argon2.IDKey(password, salt, time, memory, threads, keyLen)
}

// decodePHC parses "$argon2id$v=19$m=65536,t=1,p=4$<salt>$<hash>".
func decodePHC(encoded string) (salt, key []byte, time, memory uint32, threads uint8, err error) {
	parts := strings.Split(encoded, "$")
	// parts[0] is "" because the string starts with '$'
	if len(parts) != 6 || parts[1] != "argon2id" {
		return nil, nil, 0, 0, 0, fmt.Errorf("unsupported password hash format")
	}
	var version int
	if version, err = strconv.Atoi(parts[2][2:]); err != nil {
		return nil, nil, 0, 0, 0, fmt.Errorf("bad argon2 version: %w", err)
	}
	if version != argon2Version {
		return nil, nil, 0, 0, 0, fmt.Errorf("unsupported argon2 version %d", version)
	}
	var t, m, p int64
	if _, err = fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return nil, nil, 0, 0, 0, fmt.Errorf("bad argon2 params: %w", err)
	}
	if salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil {
		return nil, nil, 0, 0, 0, fmt.Errorf("bad salt: %w", err)
	}
	if key, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil {
		return nil, nil, 0, 0, 0, fmt.Errorf("bad key: %w", err)
	}
	return salt, key, uint32(t), uint32(m), uint8(p), nil
}
