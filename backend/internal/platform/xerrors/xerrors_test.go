package xerrors

import (
	"errors"
	"testing"
)

func TestNotImplemented_WrapsTypedSentinel(t *testing.T) {
	err := NotImplemented("k8s runner adapter")
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatal("NotImplemented must wrap ErrNotImplemented")
	}
	if got := err.Error(); got != "k8s runner adapter: not implemented" {
		t.Fatalf("Error() = %q", got)
	}
}
