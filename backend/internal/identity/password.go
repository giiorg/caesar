package identity

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Pinned argon2id parameters (plan header; OWASP baseline). Verification
// reads parameters from the stored PHC string, so raising these later
// never invalidates existing hashes.
const (
	argonMemoryKiB uint32 = 19456
	argonTime      uint32 = 2
	argonThreads   uint8  = 1
	argonSaltLen          = 16
	argonKeyLen    uint32 = 32

	// BYTES, not characters: validatePassword and the two length guards on
	// the login and change paths are all `len()`. The upper bound is a
	// denial-of-service bound on argon2id's input, and bytes are what argon2id
	// actually hashes, so counting runes here would let a multi-byte password
	// past the bound it exists to enforce.
	MinPasswordLen = 12
	MaxPasswordLen = 512
)

var b64 = base64.RawStdEncoding

// HashPassword returns the PHC-encoded argon2id hash of pw.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("identity: salt: %w", err)
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword recomputes pw under the parameters embedded in phc and
// compares in constant time.
func VerifyPassword(phc, pw string) (bool, error) {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, errors.New("identity: malformed password hash")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errors.New("identity: unsupported hash version")
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, errors.New("identity: malformed hash parameters")
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, errors.New("identity: malformed hash salt")
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil {
		return false, errors.New("identity: malformed hash key")
	}
	// want is decoded from a PHC hash we always produce via HashPassword,
	// which fixes the key at argonKeyLen (32) bytes, so len(want) never
	// approaches uint32's range.
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want))) //nolint:gosec // fixed-length key, see comment above
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
