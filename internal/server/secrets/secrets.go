// Package secrets provides symmetric encryption for credentials stored at rest
// (currently MCP server header credentials and OAuth tokens). It uses
// AES-256-GCM with a random per-message nonce prepended to the ciphertext. The
// master key is supplied by the server operator via the HARLEQUIN_SECRET_KEY
// environment variable (see internal/server/config).
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// KeySize is the required master-key length in bytes (AES-256).
const KeySize = 32

// ErrNoCipher is returned by helpers when encryption is required but no key was
// configured (the server was started without HARLEQUIN_SECRET_KEY).
var ErrNoCipher = errors.New("secrets: no encryption key configured (set HARLEQUIN_SECRET_KEY)")

// Cipher encrypts and decrypts small secrets with AES-256-GCM.
type Cipher struct {
	aead cipher.AEAD
}

// New constructs a Cipher from a 32-byte key.
func New(key []byte) (*Cipher, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("secrets: key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

// DecodeKey parses a 32-byte key encoded as hex (64 chars) or base64 (standard
// or URL, with or without padding). Hex is detected by length.
func DecodeKey(s string) ([]byte, error) {
	if len(s) == hex.EncodedLen(KeySize) {
		if b, err := hex.DecodeString(s); err == nil {
			return b, nil
		}
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == KeySize {
			return b, nil
		}
	}
	return nil, fmt.Errorf("secrets: key must be hex (%d chars) or base64 of %d bytes", hex.EncodedLen(KeySize), KeySize)
}

// Encrypt seals plaintext, returning nonce||ciphertext. A nil Cipher returns
// ErrNoCipher so callers can fail closed.
func (c *Cipher) Encrypt(plaintext []byte) ([]byte, error) {
	if c == nil {
		return nil, ErrNoCipher
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return c.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt opens nonce||ciphertext produced by Encrypt.
func (c *Cipher) Decrypt(data []byte) ([]byte, error) {
	if c == nil {
		return nil, ErrNoCipher
	}
	ns := c.aead.NonceSize()
	if len(data) < ns {
		return nil, errors.New("secrets: ciphertext too short")
	}
	nonce, ct := data[:ns], data[ns:]
	return c.aead.Open(nil, nonce, ct, nil)
}

// EncryptString is a convenience wrapper over Encrypt for string values.
func (c *Cipher) EncryptString(s string) ([]byte, error) { return c.Encrypt([]byte(s)) }

// DecryptString is a convenience wrapper over Decrypt returning a string.
func (c *Cipher) DecryptString(data []byte) (string, error) {
	b, err := c.Decrypt(data)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
