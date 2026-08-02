package config

import "os"

func allowed() string { return os.Getenv("DATABASE_URL") } // no diagnostic: this is the one allowed package
