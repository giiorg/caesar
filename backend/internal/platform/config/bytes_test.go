package config

import (
	"encoding/json"
	"testing"
)

func TestBytes_ParsesBinarySuffixes(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{`"1024"`, 1024},
		{`"512KiB"`, 512 << 10},
		{`"16MiB"`, 16 << 20},
		{`"2GiB"`, 2 << 30},
		{`"1TiB"`, 1 << 40},
	}
	for _, c := range cases {
		var b Bytes
		if err := json.Unmarshal([]byte(c.in), &b); err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if b.Int64() != c.want {
			t.Errorf("%s: got %d want %d", c.in, b.Int64(), c.want)
		}
	}
}

func TestBytes_RejectsGarbage(t *testing.T) {
	for _, in := range []string{`"16MB"`, `"-1KiB"`, `""`, `"KiB"`, `16`, `"1.5GiB"`} {
		var b Bytes
		if err := json.Unmarshal([]byte(in), &b); err == nil {
			t.Errorf("%s: expected error, got %d", in, b.Int64())
		}
	}
}

func TestBytes_RejectsOverflow(t *testing.T) {
	// The suffix multiply must not wrap int64 negative past the n < 0 guard;
	// a value this large has to error, while an in-range value still parses.
	if err := json.Unmarshal([]byte(`"999999999TiB"`), new(Bytes)); err == nil {
		t.Error(`"999999999TiB": expected overflow error, got none`)
	}
	var b Bytes
	if err := json.Unmarshal([]byte(`"7TiB"`), &b); err != nil {
		t.Fatalf(`"7TiB": %v`, err)
	}
	if b.Int64() != 7<<40 {
		t.Errorf(`"7TiB": got %d want %d`, b.Int64(), int64(7)<<40)
	}
}
