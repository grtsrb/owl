package auth

import (
	"context"
	"strings"
	"testing"
)

func TestPasswordHash(t *testing.T) {
	ctx := context.Background()
	h, err := HashPassword(ctx, "correct horse battery staple")
	if err != nil || !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("hash: %q %v", h, err)
	}
	h2, _ := HashPassword(ctx, "correct horse battery staple")
	if h == h2 {
		t.Error("salts must differ")
	}
	if ok, err := VerifyPassword(ctx, h, "correct horse battery staple"); !ok || err != nil {
		t.Errorf("right password: %v %v", ok, err)
	}
	if ok, _ := VerifyPassword(ctx, h, "correct horse battery stapler"); ok {
		t.Error("wrong password accepted")
	}
	if ok, err := VerifyPassword(ctx, "", "anything"); ok || err != nil {
		t.Errorf("no hash never matches: %v %v", ok, err)
	}
	if _, err := VerifyPassword(ctx, "$argon2id$v=19$m=99999999,t=3,p=2$AAAA$AAAA", "x"); err == nil {
		t.Error("absurd parameters must be refused (memory DoS)")
	}
	for _, pw := range []string{"short", strings.Repeat("x", MaxPasswordLen+1)} {
		if _, err := HashPassword(ctx, pw); err == nil {
			t.Errorf("rule not enforced for %d chars", len(pw))
		}
	}
	if CheckPasswordRule("twelve chars") != nil || CheckPasswordRule("ünïcödé-pässwörd") != nil {
		t.Error("valid passwords refused")
	}
}

func TestNewSecret(t *testing.T) {
	a, ha := NewSecret()
	b, _ := NewSecret()
	if a == b || len(a) != 43 || ha != Hash(a) {
		t.Errorf("secret: %q %q", a, ha)
	}
}
