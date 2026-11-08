package auth

import (
	"strings"
	"testing"
)

func TestLocalPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/": true, "/docks?x=1": true, "/conversations/abc#m": true,
		"": false, "docks": false, "//evil.example": false, "/\\evil.example": false,
		"https://evil.example": false, "/a\r\nSet-Cookie: x": false, "/" + strings.Repeat("a", MaxNext): false,
	} {
		if got := LocalPath(p); got != want {
			t.Errorf("LocalPath(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestSealer(t *testing.T) {
	if _, err := NewSealer("short"); err == nil {
		t.Fatal("short key accepted")
	}
	s, err := NewSealer(strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	sealed := s.Seal("a", []byte("secret"))
	if strings.Contains(sealed, "secret") {
		t.Fatal("not encrypted")
	}
	if got, err := s.Open("a", sealed); err != nil || string(got) != "secret" {
		t.Fatalf("open = %q, %v", got, err)
	}
	if _, err := s.Open("b", sealed); err == nil {
		t.Fatal("opened under another purpose")
	}
	other, _ := NewSealer(strings.Repeat("x", 32))
	if _, err := other.Open("a", sealed); err == nil {
		t.Fatal("opened with another key")
	}
	if _, err := s.Open("a", sealed[:len(sealed)-2]+"AA"); err == nil {
		t.Fatal("tampered value opened")
	}
}

func TestPKCE(t *testing.T) {
	v, c := NewPKCE()
	if len(v) < 43 || v == c || strings.ContainsAny(c, "+/=") {
		t.Fatalf("verifier %q challenge %q", v, c)
	}
}
