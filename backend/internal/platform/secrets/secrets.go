// Package secrets implements caesar's credential envelope encryption
// (spec §6): AES-256-GCM per-row DEKs wrapped by a master keyset from
// CAESAR_MASTER_KEY. The first keyset entry is active (encrypts new
// material); the rest are decrypt-only, which is the whole rotation
// story: add a new first key, `caesar admin rekey`, drop the old one.
// KMS/age slot in behind this same surface later.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	keyLen   = 32
	nonceLen = 12
)

var keyIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type masterKey struct {
	id   string
	aead cipher.AEAD
}

// Keyset is the parsed master keyset: one active key, any number of
// decrypt-only predecessors.
type Keyset struct {
	active masterKey
	byID   map[string]masterKey
}

// ParseKeyset parses "id:base64key[,id:base64key...]" — first entry active.
func ParseKeyset(s string) (*Keyset, error) {
	if strings.TrimSpace(s) == "" {
		return nil, errors.New("secrets: empty keyset")
	}
	ks := &Keyset{byID: map[string]masterKey{}}
	for i, part := range strings.Split(s, ",") {
		id, b64, ok := strings.Cut(strings.TrimSpace(part), ":")
		if !ok {
			return nil, fmt.Errorf("secrets: entry %d: want id:base64key", i+1)
		}
		if !keyIDPattern.MatchString(id) {
			return nil, fmt.Errorf("secrets: entry %d: key id must match %s", i+1, keyIDPattern)
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("secrets: entry %d: %w", i+1, err)
		}
		if len(raw) != keyLen {
			return nil, fmt.Errorf("secrets: entry %d: key is %d bytes, want %d", i+1, len(raw), keyLen)
		}
		if _, dup := ks.byID[id]; dup {
			return nil, fmt.Errorf("secrets: duplicate key id %q", id)
		}
		aead, err := newAEAD(raw)
		if err != nil {
			return nil, fmt.Errorf("secrets: entry %d: %w", i+1, err)
		}
		mk := masterKey{id: id, aead: aead}
		ks.byID[id] = mk
		if i == 0 {
			ks.active = mk
		}
	}
	return ks, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// ActiveKeyID names the key new envelopes are sealed under.
func (k *Keyset) ActiveKeyID() string { return k.active.id }

// Envelope is one sealed secret: a per-row DEK wrapped by master key
// KeyID, and the payload under the DEK. Both blobs are nonce||gcm-output
// with the same AAD (the credential id).
type Envelope struct {
	KeyID      string
	WrappedDEK []byte
	Ciphertext []byte
}

func gcmSeal(aead cipher.AEAD, plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("secrets: nonce: %w", err)
	}
	return append(nonce, aead.Seal(nil, nonce, plaintext, aad)...), nil
}

func gcmOpen(aead cipher.AEAD, blob, aad []byte) ([]byte, error) {
	if len(blob) < nonceLen {
		return nil, errors.New("secrets: ciphertext too short")
	}
	return aead.Open(nil, blob[:nonceLen], blob[nonceLen:], aad)
}

// Seal envelope-encrypts plaintext under a fresh DEK wrapped by the
// active master key.
func (k *Keyset) Seal(plaintext, aad []byte) (Envelope, error) {
	dek := make([]byte, keyLen)
	if _, err := rand.Read(dek); err != nil {
		return Envelope{}, fmt.Errorf("secrets: dek: %w", err)
	}
	dekAEAD, err := newAEAD(dek)
	if err != nil {
		return Envelope{}, err
	}
	ct, err := gcmSeal(dekAEAD, plaintext, aad)
	if err != nil {
		return Envelope{}, err
	}
	wrapped, err := gcmSeal(k.active.aead, dek, aad)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{KeyID: k.active.id, WrappedDEK: wrapped, Ciphertext: ct}, nil
}

// Open reverses Seal using whichever keyset entry sealed e. Every failure
// is the same closed error shape (no tamper oracle beyond GCM's own).
func (k *Keyset) Open(e Envelope, aad []byte) ([]byte, error) {
	mk, ok := k.byID[e.KeyID]
	if !ok {
		return nil, fmt.Errorf("secrets: no master key %q in keyset (rotation dropped it too early?)", e.KeyID)
	}
	dek, err := gcmOpen(mk.aead, e.WrappedDEK, aad)
	if err != nil {
		return nil, fmt.Errorf("secrets: unwrap dek: %w", err)
	}
	dekAEAD, err := newAEAD(dek)
	if err != nil {
		return nil, err
	}
	plain, err := gcmOpen(dekAEAD, e.Ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("secrets: open: %w", err)
	}
	return plain, nil
}
