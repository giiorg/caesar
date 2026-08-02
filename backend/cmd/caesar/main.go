package main

import (
	"os"

	"github.com/giiorg/caesar/backend/internal/app"
)

func main() {
	os.Exit(app.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
