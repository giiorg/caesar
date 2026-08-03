// Package xerrors holds caesar's cross-cutting error sentinels. Stubs must
// return NotImplemented — never a fake success (decisions §6.13); the
// stubfake analyzer points violators here.
package xerrors

import (
	"errors"
	"fmt"
)

var ErrNotImplemented = errors.New("not implemented")

func NotImplemented(what string) error {
	return fmt.Errorf("%s: %w", what, ErrNotImplemented)
}

// Class is the internal error taxonomy's first axis (spec §4). The zero
// value is ClassInternal: an unclassified error is always treated as an
// internal fault, never leaked to clients (default-deny).
type Class int

const (
	ClassInternal Class = iota
	ClassInvalid
	ClassUnauthenticated
	ClassForbidden
	ClassNotFound
	ClassConflict
	ClassRateLimited
)

// FieldError is one field-level validation failure, surfaced verbatim in
// the API problem envelope.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Error is a classified error. Msg is safe for API clients; Err is the
// internal cause and never crosses the API boundary.
type Error struct {
	Class  Class
	Msg    string
	Fields []FieldError
	Err    error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return e.Msg + ": " + e.Err.Error()
	}
	return e.Msg
}

func (e *Error) Unwrap() error { return e.Err }

// WithFields returns e with field errors attached (builder style).
func (e *Error) WithFields(fields ...FieldError) *Error {
	e.Fields = append(e.Fields, fields...)
	return e
}

func E(c Class, msg string) *Error { return &Error{Class: c, Msg: msg} }

func Wrap(c Class, msg string, err error) *Error { return &Error{Class: c, Msg: msg, Err: err} }

// ClassOf walks the error chain for a classified error; anything else is
// internal.
func ClassOf(err error) Class {
	var e *Error
	if errors.As(err, &e) {
		return e.Class
	}
	return ClassInternal
}
