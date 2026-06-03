package secrets

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func newTestCipher(t *testing.T) (*Cipher, []byte) {
	t.Helper()
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	c, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	return c, key
}

func TestRoundTrip(t *testing.T) {
	c, _ := newTestCipher(t)
	for _, pt := range []string{"", "hello", "Bearer sk-abc123", "a longer secret with unicode ✓"} {
		ct, err := c.EncryptString(pt)
		if err != nil {
			t.Fatalf("encrypt %q: %v", pt, err)
		}
		got, err := c.DecryptString(ct)
		if err != nil {
			t.Fatalf("decrypt %q: %v", pt, err)
		}
		if got != pt {
			t.Fatalf("round-trip mismatch: got %q want %q", got, pt)
		}
	}
}

func TestNonceUnique(t *testing.T) {
	c, _ := newTestCipher(t)
	a, _ := c.EncryptString("same")
	b, _ := c.EncryptString("same")
	if bytes.Equal(a, b) {
		t.Fatal("ciphertexts should differ due to random nonce")
	}
}

func TestTamperDetection(t *testing.T) {
	c, _ := newTestCipher(t)
	ct, _ := c.EncryptString("secret")
	ct[len(ct)-1] ^= 0xff
	if _, err := c.Decrypt(ct); err == nil {
		t.Fatal("expected auth failure on tampered ciphertext")
	}
}

func TestWrongKey(t *testing.T) {
	c1, _ := newTestCipher(t)
	c2, _ := newTestCipher(t)
	ct, _ := c1.EncryptString("secret")
	if _, err := c2.Decrypt(ct); err == nil {
		t.Fatal("expected decryption failure with wrong key")
	}
}

func TestNilCipherFailsClosed(t *testing.T) {
	var c *Cipher
	if _, err := c.Encrypt([]byte("x")); err != ErrNoCipher {
		t.Fatalf("got %v want ErrNoCipher", err)
	}
	if _, err := c.Decrypt([]byte("x")); err != ErrNoCipher {
		t.Fatalf("got %v want ErrNoCipher", err)
	}
}

func TestNewBadKeySize(t *testing.T) {
	if _, err := New([]byte("short")); err == nil {
		t.Fatal("expected error for short key")
	}
}
