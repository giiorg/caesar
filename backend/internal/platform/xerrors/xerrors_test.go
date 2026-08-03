package xerrors

import (
	"errors"
	"fmt"
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

func TestClassOf_TableDriven(t *testing.T) {
	wrapped := Wrap(ClassConflict, "already exists", errors.New("duplicate key"))
	tests := []struct {
		name string
		err  error
		want Class
	}{
		{"typed error", E(ClassNotFound, "no such user"), ClassNotFound},
		{"wrapped typed error", fmt.Errorf("outer: %w", wrapped), ClassConflict},
		{"plain error defaults to internal", errors.New("boom"), ClassInternal},
		{"not-implemented sentinel defaults to internal", NotImplemented("thing"), ClassInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassOf(tt.err); got != tt.want {
				t.Fatalf("ClassOf() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestError_MessageAndUnwrap(t *testing.T) {
	cause := errors.New("pq: duplicate key")
	err := Wrap(ClassConflict, "email already registered", cause)
	if got := err.Error(); got != "email already registered: pq: duplicate key" {
		t.Fatalf("Error() = %q", got)
	}
	if !errors.Is(err, cause) {
		t.Fatal("errors.Is should reach the wrapped cause")
	}
	if E(ClassInvalid, "bad").Error() != "bad" {
		t.Fatal("unwrapped Error() should be the bare message")
	}
}

func TestWithFields_CarriesFieldErrors(t *testing.T) {
	err := E(ClassInvalid, "validation failed").
		WithFields(FieldError{Field: "email", Message: "not a valid address"})
	if len(err.Fields) != 1 || err.Fields[0].Field != "email" {
		t.Fatalf("Fields = %+v", err.Fields)
	}
}
