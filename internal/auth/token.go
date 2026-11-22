package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// tokenBytes is the size of every random token (session, API, invite).
const tokenBytes = 32

// NewToken returns a fresh random token for the client (base64url, no
// padding) and the SHA-256 hash that is the only thing stored.
func NewToken() (token string, hash []byte) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error()) // never happens on supported platforms
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token)
}

// HashToken is the stored form of a client token. Hashing the string the
// client holds (rather than the decoded bytes) means a malformed token simply
// matches nothing.
func HashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}
