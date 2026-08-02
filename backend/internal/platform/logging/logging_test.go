package logging

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestNew_EmitsOneJSONObjectPerLine(t *testing.T) {
	var buf bytes.Buffer
	log, err := New(&buf, "info")
	if err != nil {
		t.Fatal(err)
	}
	log.Info("hello", "answer", 42)

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("output is not JSON: %v (raw: %s)", err, buf.String())
	}
	if line["msg"] != "hello" || line["level"] != "INFO" || line["answer"] != float64(42) {
		t.Fatalf("unexpected line: %v", line)
	}
}

func TestNew_FiltersBelowConfiguredLevel(t *testing.T) {
	var buf bytes.Buffer
	log, err := New(&buf, "warn")
	if err != nil {
		t.Fatal(err)
	}
	log.Info("dropped")
	if buf.Len() != 0 {
		t.Fatalf("info line should be filtered at warn, got: %s", buf.String())
	}
}

func TestNew_RejectsUnknownLevel(t *testing.T) {
	if _, err := New(&bytes.Buffer{}, "loud"); err == nil {
		t.Fatal("want error for unknown level")
	}
}
