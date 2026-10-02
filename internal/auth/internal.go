package auth

import "crypto/rand"

// randomRead is a seam for tests; production uses crypto/rand.
var randomRead = rand.Read

// argon2Version matches golang.org/x/crypto/argon2's version tag (19).
const argon2Version = 19
