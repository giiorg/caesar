package runner

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// brokerTokenPrefix versions the token format so the gateway can reject
// anything it does not recognize outright rather than guessing.
const brokerTokenPrefix = "cbt1."

// newBrokerToken mints one attempt's broker credential and its digest: the
// token goes to the agent, only the SHA-256 goes to the gateway, so a leaked
// gateway config yields nothing that can be replayed.
func newBrokerToken() (token, sha256hex string, err error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", fmt.Errorf("runner: broker token: %w", err)
	}
	token = brokerTokenPrefix + base64.RawURLEncoding.EncodeToString(b[:])
	sum := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(sum[:]), nil
}
