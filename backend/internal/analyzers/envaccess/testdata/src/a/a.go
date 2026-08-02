package a

import "os"

func bad() {
	_ = os.Getenv("HOME")     // want `environment access outside internal/platform/config`
	_, _ = os.LookupEnv("X")  // want `environment access outside internal/platform/config`
	_ = os.Environ()          // want `environment access outside internal/platform/config`
}
