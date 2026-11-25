package secretbox

import (
	"bytes"
	"strings"
	"testing"
)

func TestSealOpen(t *testing.T) {
	b, err := ParseKey("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err != nil {
		t.Fatal(err)
	}
	s1, s2 := b.Seal([]byte("pw"), []byte("a")), b.Seal([]byte("pw"), []byte("a"))
	if bytes.Equal(s1, s2) || bytes.Contains(s1, []byte("pw")) {
		t.Fatal("nonce reused or plaintext visible")
	}
	if p, err := b.Open(s1, []byte("a")); err != nil || string(p) != "pw" {
		t.Fatalf("open: %q %v", p, err)
	}
	if _, err := b.Open(s1, []byte("b")); err != ErrOpen {
		t.Fatal("opened with other additional data")
	}
	other, _ := New(bytes.Repeat([]byte{1}, 32))
	if _, err := other.Open(s1, []byte("a")); err != ErrOpen {
		t.Fatal("opened with another key")
	}
	if _, err := b.Open(s1[:5], []byte("a")); err != ErrOpen {
		t.Fatal("short input")
	}
}

func TestParseKeyErrorsDontQuote(t *testing.T) {
	for _, k := range []string{"", "c2hvcnQ=", "not base64 secretvalue!!"} {
		_, err := ParseKey(k)
		if err == nil || (k != "" && strings.Contains(err.Error(), k)) {
			t.Fatalf("%q: %v", k, err)
		}
	}
}
