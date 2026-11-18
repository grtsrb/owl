// Package auth handles agent tokens, personal API tokens, sessions secrets and passwords.
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

// APITokenPrefix marks personal API tokens (milestone 12), so they're recognisable in a config file
// or a leaked log (and by secret scanners), and never confused with agent tokens.
const APITokenPrefix = "owl_pat_"

// NewAPIToken returns a random personal API token, its storage hash and a display prefix.
func NewAPIToken() (token, hash, display string) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	token = APITokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	return token, Hash(token), token[:len(APITokenPrefix)+6]
}

// IsAPIToken: the value looks like a personal API token (prefix and length; checked before any lookup).
func IsAPIToken(t string) bool {
	return strings.HasPrefix(t, APITokenPrefix) && len(t) == len(APITokenPrefix)+43
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
