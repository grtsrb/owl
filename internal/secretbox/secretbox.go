// Package secretbox encrypts secrets Owl has to use itself (registry credentials, milestone 15.1) with a
// key from the environment (OWL_SECRET_KEY), so the database file and its backups hold only ciphertext.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
)

// Box seals and opens values with AES-256-GCM.
type Box struct{ aead cipher.AEAD }

// ErrOpen: the value wasn't sealed with this key (or for this purpose), or was changed.
var ErrOpen = errors.New("can't decrypt (another OWL_SECRET_KEY?)")

// ParseKey reads a base64 32-byte key ("openssl rand -base64 32"). Errors never quote the key.
func ParseKey(s string) (*Box, error) {
	s = strings.TrimSpace(s)
	k, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		k, err = base64.RawStdEncoding.DecodeString(s)
	}
	if err != nil || len(k) != 32 {
		return nil, errors.New("OWL_SECRET_KEY must be 32 random bytes, base64 (openssl rand -base64 32)")
	}
	return New(k)
}

// New makes a Box from a 32-byte key.
func New(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, errors.New("secretbox: key must be 32 bytes")
	}
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	a, err := cipher.NewGCM(b)
	if err != nil {
		return nil, err
	}
	return &Box{aead: a}, nil
}

// Seal encrypts plain, bound to ad (e.g. what the value belongs to: a ciphertext opens only with the same ad).
func (b *Box) Seal(plain, ad []byte) []byte {
	nonce := make([]byte, b.aead.NonceSize(), b.aead.NonceSize()+len(plain)+b.aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return b.aead.Seal(nonce, nonce, plain, ad)
}

// Open decrypts what Seal returned for the same ad.
func (b *Box) Open(sealed, ad []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n+b.aead.Overhead() {
		return nil, ErrOpen
	}
	p, err := b.aead.Open(nil, sealed[:n], sealed[n:], ad)
	if err != nil {
		return nil, ErrOpen
	}
	return p, nil
}
