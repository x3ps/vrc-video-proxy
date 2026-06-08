package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// URLSigner seals an opaque payload (an absolute upstream URL, or a JSON blob
// carrying a URL plus the request headers to replay) together with an expiry into
// a token. The HLS/DASH manifest rewriter embeds these tokens in the URLs it
// hands to the player, so the segment endpoint only ever fetches URLs the server
// itself signed — the proxy cannot be used as an open proxy (cf. MediaFlow's
// _token_ path segment, but here AES-GCM also hides the origin). Decoded payloads
// are still passed through guardUpstreamURL as defence in depth.
type URLSigner struct {
	aead cipher.AEAD
}

// signerTimeNow is the clock used for expiry, overridable in tests.
var signerTimeNow = time.Now

// NewURLSigner derives an AES-256-GCM key from secret. An empty secret generates
// a random key valid only for the current process lifetime (tokens do not
// survive a restart, which is fine: the player re-fetches the manifest).
func NewURLSigner(secret string) (*URLSigner, error) {
	var key [32]byte
	if secret == "" {
		if _, err := rand.Read(key[:]); err != nil {
			return nil, fmt.Errorf("generate signer key: %w", err)
		}
	} else {
		key = sha256.Sum256([]byte(secret))
	}

	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("init cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("init gcm: %w", err)
	}
	return &URLSigner{aead: aead}, nil
}

// Sign returns an opaque token for payload that expires after ttl. A ttl <= 0
// means the token never expires.
func (s *URLSigner) Sign(payload string, ttl time.Duration) string {
	var expiry int64 // 0 == no expiry
	if ttl > 0 {
		expiry = signerTimeNow().Add(ttl).Unix()
	}

	plaintext := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint64(plaintext[:8], uint64(expiry))
	copy(plaintext[8:], payload)

	nonce := make([]byte, s.aead.NonceSize())
	// crypto/rand never fails in practice; on the impossible error path we fall
	// back to a zero nonce, which GCM still accepts (uniqueness is best-effort
	// for confidentiality only — integrity and expiry are unaffected).
	_, _ = rand.Read(nonce)

	sealed := s.aead.Seal(nonce, nonce, plaintext, nil)
	return base64.RawURLEncoding.EncodeToString(sealed)
}

// errTokenExpired is returned when a token's embedded expiry has passed.
var errTokenExpired = errors.New("token expired")

// Verify decodes a token, checking integrity and expiry, and returns the
// original payload. Any tampering, truncation, or wrong key yields an error.
func (s *URLSigner) Verify(token string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", fmt.Errorf("decode token: %w", err)
	}
	nonceSize := s.aead.NonceSize()
	if len(raw) < nonceSize {
		return "", errors.New("token too short")
	}

	nonce, ciphertext := raw[:nonceSize], raw[nonceSize:]
	plaintext, err := s.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("open token: %w", err)
	}
	if len(plaintext) < 8 {
		return "", errors.New("token payload too short")
	}

	expiry := int64(binary.BigEndian.Uint64(plaintext[:8]))
	if expiry != 0 && signerTimeNow().Unix() > expiry {
		return "", errTokenExpired
	}
	return string(plaintext[8:]), nil
}
