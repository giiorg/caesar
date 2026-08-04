package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestBrokerToken_FormatAndEntropy(t *testing.T) {
	tok, sha, err := newBrokerToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok, "cbt1.") || len(tok) != len("cbt1.")+32 { // 24B base64url = 32 chars
		t.Fatalf("token = %q", tok)
	}
	sum := sha256.Sum256([]byte(tok))
	if sha != hex.EncodeToString(sum[:]) {
		t.Fatal("sha mismatch")
	}
	tok2, _, _ := newBrokerToken()
	if tok == tok2 {
		t.Fatal("tokens must be random")
	}
}
