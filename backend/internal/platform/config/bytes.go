package config

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Bytes is a byte size that unmarshals from strict JSON strings like
// "16MiB" (binary suffixes only) or bare digits like "1024". Decimal
// suffixes (MB/GB) are rejected: one notation, no ambiguity.
type Bytes int64

var byteSuffixes = []struct {
	suffix string
	mult   int64
}{
	{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10},
}

func (b *Bytes) UnmarshalJSON(raw []byte) error {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf(`byte size must be a string like "16MiB": %w`, err)
	}
	mult := int64(1)
	digits := s
	for _, sf := range byteSuffixes {
		if strings.HasSuffix(s, sf.suffix) {
			mult, digits = sf.mult, strings.TrimSuffix(s, sf.suffix)
			break
		}
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n < 0 {
		return fmt.Errorf("invalid byte size %q", s)
	}
	*b = Bytes(n * mult)
	return nil
}

func (b Bytes) Int64() int64 { return int64(b) }
