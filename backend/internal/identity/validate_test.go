// In-package: validatePassword is unexported and every exported route to it
// (CreateUser, the invite redemption, the password change) needs a database,
// so an external test would pay a container to assert a string constant.
package identity

import (
	"errors"
	"strings"
	"testing"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// MaxPasswordLen is a BYTE bound — it exists to cap what argon2id hashes, and
// bytes are what argon2id hashes — while the message said "characters".
//
// The input is 300 runes against a 512-byte limit: a rune count accepts it,
// a byte count refuses it. So the refusal pins the unit and the wording pins
// what the user is told, and neither assertion can carry the other.
func TestValidatePassword_LengthLimitsAreBytesAndSaySo(t *testing.T) {
	err := validatePassword(strings.Repeat("é", 300))
	var xe *xerrors.Error
	if !errors.As(err, &xe) {
		t.Fatalf("err = %v; want a refusal: 300 two-byte runes is 600 bytes, past the %d-byte bound",
			err, MaxPasswordLen)
	}
	if len(xe.Fields) == 0 || xe.Fields[0].Field != "password" {
		t.Fatalf("fields = %+v, want one blaming \"password\"", xe.Fields)
	}
	if !strings.Contains(xe.Fields[0].Message, "bytes") {
		t.Errorf("message = %q; the check counts bytes and must say so", xe.Fields[0].Message)
	}
}
