package buildinfo

import "testing"

func TestString_ContainsAllStampedFields(t *testing.T) {
	got := String()
	want := "caesar dev (commit none, built unknown)"
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
