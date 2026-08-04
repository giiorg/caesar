package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/giiorg/caesar/backend/internal/platform/dockerapi"
)

func TestFailureSnapshot_ShapeTailAndRedaction(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "agent.log")
	big := strings.Repeat("x", 100<<10) + "SECRET-cbt1.abc token line\nlast line\n"
	if err := os.WriteFile(logPath, []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("aaa"), 0o600); err != nil {
		t.Fatal(err)
	}

	var ins dockerapi.ContainerInspect
	ins.State.ExitCode, ins.State.OOMKilled = 137, true
	snap := buildFailureSnapshot(ins, logPath, 64<<10, ws, []string{"SECRET-cbt1.abc"})

	if snap["exit_code"] != int64(137) || snap["oom_killed"] != true {
		t.Fatalf("state: %v", snap)
	}
	tail, ok := snap["log_tail"].(string)
	if !ok {
		t.Fatalf("log_tail is %T", snap["log_tail"])
	}
	if len(tail) > 64<<10 || !strings.HasSuffix(tail, "last line\n") {
		t.Fatalf("tail must be the LAST 64KiB, got %d bytes ending %q", len(tail), lastBytes(tail, 20))
	}
	if strings.Contains(tail, "SECRET-cbt1.abc") || !strings.Contains(tail, "[redacted]") {
		t.Fatal("secret not redacted")
	}
	wsInfo, ok := snap["workspace"].(map[string]any)
	if !ok {
		t.Fatalf("workspace is %T", snap["workspace"])
	}
	if wsInfo["files"] != int64(1) || len(wsInfo["entries"].([]map[string]any)) != 1 {
		t.Fatalf("workspace listing: %v", wsInfo)
	}
	if wsInfo["bytes"] != int64(3) {
		t.Errorf("workspace bytes = %v, want 3", wsInfo["bytes"])
	}
	entry := wsInfo["entries"].([]map[string]any)[0]
	if entry["path"] != "a.txt" || entry["bytes"] != int64(3) {
		t.Errorf("entry = %v, want the workspace-relative path and its size", entry)
	}
}

func TestFailureSnapshot_ListingCapsAt200Entries(t *testing.T) {
	dir := t.TempDir()
	ws := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 250 {
		if err := os.WriteFile(filepath.Join(ws, fmt.Sprintf("f%03d", i)), []byte("z"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	snap := buildFailureSnapshot(dockerapi.ContainerInspect{}, filepath.Join(dir, "absent.log"), 64<<10, ws, nil)
	wsInfo := snap["workspace"].(map[string]any)

	// The counts measure the whole tree; the listing is a sample of it.
	if wsInfo["files"] != int64(250) || wsInfo["bytes"] != int64(250) {
		t.Errorf("counts = files:%v bytes:%v, want the whole tree", wsInfo["files"], wsInfo["bytes"])
	}
	if got := len(wsInfo["entries"].([]map[string]any)); got != snapshotEntries {
		t.Errorf("entries = %d, want the cap of %d", got, snapshotEntries)
	}
	if wsInfo["entries_capped"] != true {
		t.Error("a listing that shows less than it counted must say so")
	}
}

func TestFailureSnapshot_SurvivesAMissingLogAndWorkspace(t *testing.T) {
	// The snapshot describes a failure; it must not become one. A container
	// that died before its bind mount existed leaves exactly this shape.
	dir := t.TempDir()
	snap := buildFailureSnapshot(dockerapi.ContainerInspect{}, filepath.Join(dir, "nope.log"),
		64<<10, filepath.Join(dir, "nowhere"), []string{"s3cret"})

	if snap["log_tail"] != "" {
		t.Errorf("log_tail = %v, want empty", snap["log_tail"])
	}
	wsInfo := snap["workspace"].(map[string]any)
	if wsInfo["files"] != int64(0) || len(wsInfo["entries"].([]map[string]any)) != 0 {
		t.Errorf("workspace = %v, want an empty listing", wsInfo)
	}
}

func TestFailureSnapshot_RedactsEveryValueAndSkipsTheTopGit(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "agent.log")
	if err := os.WriteFile(logPath, []byte("token=cbt1.zzz key=sk-live-1 done\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(filepath.Join(ws, ".git", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".git", "objects", "pack"), []byte("gggg"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "kept.txt"), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}

	var ins dockerapi.ContainerInspect
	ins.State.Error = "oci runtime error near cbt1.zzz"
	snap := buildFailureSnapshot(ins, logPath, 64<<10, ws, []string{"cbt1.zzz", "sk-live-1", ""})

	tail := snap["log_tail"].(string)
	if strings.Contains(tail, "cbt1.zzz") || strings.Contains(tail, "sk-live-1") {
		t.Errorf("log_tail leaked a secret: %q", tail)
	}
	if got, want := tail, "token=[redacted] key=[redacted] done\n"; got != want {
		t.Errorf("log_tail = %q, want %q", got, want)
	}
	if err, ok := snap["error"].(string); !ok || strings.Contains(err, "cbt1.zzz") {
		t.Errorf("state error leaked a secret: %v", snap["error"])
	}
	// The mirror the agent never owned is not part of its evidence, and the
	// quota it was measured against skipped it too.
	wsInfo := snap["workspace"].(map[string]any)
	if wsInfo["files"] != int64(1) {
		t.Errorf("files = %v, want the top .git skipped", wsInfo["files"])
	}
}

func TestFailureSnapshot_RedactsTheNamesTheAgentChose(t *testing.T) {
	// The workspace listing is agent-authored text like any other: a file
	// named after the broker token must not smuggle it into an artifact.
	dir := t.TempDir()
	ws := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "notes-cbt1.zzz.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	snap := buildFailureSnapshot(dockerapi.ContainerInspect{}, filepath.Join(dir, "absent.log"),
		64<<10, ws, []string{"cbt1.zzz"})

	entries := snap["workspace"].(map[string]any)["entries"].([]map[string]any)
	if len(entries) != 1 {
		t.Fatalf("entries = %v", entries)
	}
	if path, _ := entries[0]["path"].(string); strings.Contains(path, "cbt1.zzz") {
		t.Errorf("entry path leaked the token: %q", path)
	}
}

func lastBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
