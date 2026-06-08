package main

import (
	"errors"
	"testing"
	"time"
)

func TestURLSignerRoundTrip(t *testing.T) {
	s, err := NewURLSigner("test-secret")
	if err != nil {
		t.Fatalf("NewURLSigner: %v", err)
	}
	const url = "https://cdn.example.com/path/segment_001.ts?token=abc"
	token := s.Sign(url, time.Hour)
	got, err := s.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got != url {
		t.Fatalf("round-trip url = %q, want %q", got, url)
	}
}

func TestURLSignerExpiry(t *testing.T) {
	s, err := NewURLSigner("secret")
	if err != nil {
		t.Fatalf("NewURLSigner: %v", err)
	}

	base := time.Unix(1_000_000, 0)
	signerTimeNow = func() time.Time { return base }
	t.Cleanup(func() { signerTimeNow = time.Now })

	token := s.Sign("https://cdn/s.ts", time.Minute)

	signerTimeNow = func() time.Time { return base.Add(2 * time.Minute) }
	if _, err := s.Verify(token); !errors.Is(err, errTokenExpired) {
		t.Fatalf("expected errTokenExpired, got %v", err)
	}
}

func TestURLSignerNoExpiry(t *testing.T) {
	s, _ := NewURLSigner("secret")
	token := s.Sign("https://cdn/s.ts", 0)
	if _, err := s.Verify(token); err != nil {
		t.Fatalf("zero ttl should never expire, got %v", err)
	}
}

func TestURLSignerTamperRejected(t *testing.T) {
	s, _ := NewURLSigner("secret")
	token := s.Sign("https://cdn/s.ts", time.Hour)
	// Flip a character in the middle of the token.
	b := []byte(token)
	b[len(b)/2] ^= 0x01
	if _, err := s.Verify(string(b)); err == nil {
		t.Fatal("tampered token should not verify")
	}
}

func TestURLSignerWrongKeyRejected(t *testing.T) {
	a, _ := NewURLSigner("secret-a")
	b, _ := NewURLSigner("secret-b")
	token := a.Sign("https://cdn/s.ts", time.Hour)
	if _, err := b.Verify(token); err == nil {
		t.Fatal("token signed by a must not verify under b")
	}
}
