// Package auth handles agent tokens.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

const tokenPrefix = "inv_"

// NewToken returns a random agent token (shown to the user once), its storage hash and a short
// non-secret display prefix.
func NewToken() (token, hash, display string) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failing means the system is unusable
	}
	token = tokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	return token, Hash(token), token[:len(tokenPrefix)+6]
}

// Hash is SHA-256: tokens are 256 bits of randomness, so a slow password hash adds nothing and
// a plain hash allows indexed lookup.
func Hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// BearerToken extracts the token from an Authorization header value.
func BearerToken(header string) (string, bool) {
	const p = "Bearer "
	if len(header) <= len(p) || !strings.EqualFold(header[:len(p)], p) {
		return "", false
	}
	t := strings.TrimSpace(header[len(p):])
	return t, strings.HasPrefix(t, tokenPrefix)
}
