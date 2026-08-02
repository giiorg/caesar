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
