package a

import (
	"errors"
	"fmt"
)

func bad1() error { return errors.New("not implemented") } // want `use platform/xerrors.NotImplemented`
func bad2() error { return fmt.Errorf("frobnicator Not Implemented yet") } // want `use platform/xerrors.NotImplemented`
func bad3() { panic("not implemented") } // want `use platform/xerrors.NotImplemented`
func ok() error { return errors.New("widget exploded") }
