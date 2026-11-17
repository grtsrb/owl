package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Password rules: length is what matters (NIST 800-63B); no composition rules. The upper bound keeps
// hashing cost predictable.
const (
	MinPasswordLen = 12
	MaxPasswordLen = 256
)

// argon2id parameters (RFC 9106's second recommendation, with 2 lanes): ~64 MiB and well under a
// second per hash on a small instance. hashSlots bounds how many run at once, so a burst of logins
// can't exhaust memory (2 × 64 MiB).
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 2
	argonKeyLen  = 32
	saltLen      = 16
)

var hashSlots = make(chan struct{}, 2)

var ErrPasswordRule = fmt.Errorf("password must be %d to %d characters", MinPasswordLen, MaxPasswordLen)

// CheckPasswordRule validates a new password.
func CheckPasswordRule(pw string) error {
	if n := utf8.RuneCountInString(pw); n < MinPasswordLen || n > MaxPasswordLen || !utf8.ValidString(pw) {
		return ErrPasswordRule
	}
	return nil
}

func argonKey(ctx context.Context, pw string, salt []byte, t, m uint32, p uint8, n uint32) ([]byte, error) {
	select {
	case hashSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-hashSlots }()
	return argon2.IDKey([]byte(pw), salt, t, m, p, n), nil
}

// HashPassword returns an argon2id hash in the PHC string format ($argon2id$v=19$m=…,t=…,p=…$salt$key).
func HashPassword(ctx context.Context, pw string) (string, error) {
	if err := CheckPasswordRule(pw); err != nil {
		return "", err
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := argonKey(ctx, pw, salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	if err != nil {
		return "", err
	}
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

var errBadHash = errors.New("unsupported password hash")

// dummyHash is checked against when there is no user (or no password), so a login attempt takes as
// long for an unknown email as for a wrong password — response time doesn't reveal who has an account.
// Computed on first use (not at startup: the same binary runs the agent).
var dummyHash = sync.OnceValue(func() string {
	h, err := HashPassword(context.Background(), "owl-dummy-password-for-timing")
	if err != nil {
		panic(err)
	}
	return h
})

// VerifyPassword reports whether pw matches the PHC hash. An empty hash (no password set) is checked
// against a dummy hash and never matches.
func VerifyPassword(ctx context.Context, hash, pw string) (bool, error) {
	if hash == "" || len(pw) > 4*MaxPasswordLen {
		_, _ = verify(ctx, dummyHash(), "x")
		return false, nil
	}
	return verify(ctx, hash, pw)
}

func verify(ctx context.Context, hash, pw string) (bool, error) {
	parts := strings.Split(hash, "$") // "", "argon2id", "v=19", "m=…,t=…,p=…", salt, key
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errBadHash
	}
	var v int
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return false, errBadHash
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m == 0 || m > 1<<20 || t == 0 || t > 10 || p == 0 {
		return false, errBadHash
	}
	b64 := base64.RawStdEncoding
	salt, err1 := b64.DecodeString(parts[4])
	want, err2 := b64.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(want) == 0 || len(want) > 64 {
		return false, errBadHash
	}
	got, err := argonKey(ctx, pw, salt, t, m, p, uint32(len(want)))
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// NewSecret returns 256 random bits, URL-safe base64 (session cookies, invite/reset links), and the
// SHA-256 that is stored instead.
func NewSecret() (secret, hash string) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	secret = base64.RawURLEncoding.EncodeToString(b)
	return secret, Hash(secret)
}
