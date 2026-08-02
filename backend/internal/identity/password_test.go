package identity

import (
	"strings"
	"testing"
)

func TestHashPassword_ProducesVerifiablePHC(t *testing.T) {
	phc, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(phc, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("unexpected PHC prefix: %s", phc)
	}
	ok, err := VerifyPassword(phc, "correct horse battery staple")
	if err != nil || !ok {
		t.Fatalf("verify(correct) = %v, %v", ok, err)
	}
	ok, err = VerifyPassword(phc, "wrong password entirely")
	if err != nil || ok {
		t.Fatalf("verify(wrong) = %v, %v", ok, err)
	}
}

func TestHashPassword_SaltsDiffer(t *testing.T) {
	a, err := HashPassword("same password twice")
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashPassword("same password twice")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two hashes of the same password must differ (random salt)")
	}
}

func TestVerifyPassword_RejectsMalformedPHC(t *testing.T) {
	tests := []string{
		"",
		"not a phc string",
		"$argon2i$v=19$m=19456,t=2,p=1$AAAA$AAAA",       // wrong variant
		"$argon2id$v=18$m=19456,t=2,p=1$AAAA$AAAA",      // wrong version
		"$argon2id$v=19$m=19456,t=2$AAAA$AAAA",          // missing param
		"$argon2id$v=19$m=19456,t=2,p=1$!notb64!$AAAA",  // bad salt encoding
	}
	for _, phc := range tests {
		if _, err := VerifyPassword(phc, "whatever"); err == nil {
			t.Errorf("VerifyPassword(%q) should error", phc)
		}
	}
}

// Verification must honour the parameters embedded in the stored string,
// so raising the pinned defaults later never invalidates old hashes.
func TestVerifyPassword_HonoursEmbeddedParams(t *testing.T) {
	// Hash of "legacy password!" under m=8192,t=1,p=1, salt
	// "somesaltsomesalt" — precomputed with x/crypto/argon2 v0.54.0.
	const legacy = "$argon2id$v=19$m=8192,t=1,p=1$c29tZXNhbHRzb21lc2FsdA$Jnh3G/TTu8wXQU0fIQZ0vqMqD2+ljUh3ckeadOlwHmI"
	ok, err := VerifyPassword(legacy, "legacy password!")
	if err != nil || !ok {
		t.Fatalf("legacy-params verify = %v, %v", ok, err)
	}
}
