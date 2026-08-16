//go:build integration

package app

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/giiorg/caesar/backend/internal/platform/config"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
)

// syncBuffer captures runServe's log while its goroutines are still writing.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestRunServe_BootsMigratesAndShutsDownOnCancel(t *testing.T) {
	cfg := config.Config{}
	cfg.Server.Addr = "127.0.0.1:0"
	cfg.Server.ShutdownTimeout = config.Duration(5 * time.Second)
	cfg.Database.URL = testdb.NewURL(t)
	cfg.Security.MasterKey = "dev:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	cfg.Orchestrator = config.Orchestrator{
		Workers:           2,
		PollInterval:      config.Duration(50 * time.Millisecond),
		LeaseTTL:          config.Duration(30 * time.Second),
		HeartbeatInterval: config.Duration(10 * time.Second),
		OutboxRetention:   config.Duration(168 * time.Hour),
	}

	logBuf := &syncBuffer{}
	log := slog.New(slog.NewJSONHandler(logBuf, nil))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, cfg, log) }()

	// Cancel only after boot has finished: the listening line lands after
	// migrations apply, where a fixed sleep raced a slow migrate on CI.
	bootDeadline := time.Now().Add(30 * time.Second)
	for !strings.Contains(logBuf.String(), "http server listening") {
		select {
		case err := <-done:
			t.Fatalf("runServe returned before listening: %v\n%s", err, logBuf.String())
		default:
		}
		if time.Now().After(bootDeadline) {
			t.Fatalf("server never reported listening:\n%s", logBuf.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServe = %v, want clean shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runServe did not return after cancel")
	}
}

// The publication work dir is materialized at BOOT, not lazily on the first
// publication. Lazily is the tempting version and the wrong one: it hides a
// misconfiguration until a run has already been approved and pushed, which is
// the worst possible moment to discover it.
//
// A path whose parent is a regular file is a mkdir that cannot succeed, so this
// asserts the boot-time refusal rather than a directory listing — the refusal is
// the observable half, and it can only happen if the directory is made here.
func TestRunServe_RefusesToBootOnAnUnusablePublishWorkDir(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{}
	cfg.Server.Addr = "127.0.0.1:0"
	cfg.Server.ShutdownTimeout = config.Duration(5 * time.Second)
	cfg.Database.URL = testdb.NewURL(t)
	cfg.Security.MasterKey = "dev:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	cfg.Forge.PublishWorkDir = filepath.Join(blocker, "publish")
	// A bootable orchestrator block on purpose: without it a build that skipped
	// the refusal would die on a zero poll interval, and this test would read as
	// passing for a reason that has nothing to do with the work dir.
	cfg.Orchestrator = config.Orchestrator{
		Workers:           2,
		PollInterval:      config.Duration(50 * time.Millisecond),
		LeaseTTL:          config.Duration(30 * time.Second),
		HeartbeatInterval: config.Duration(10 * time.Second),
		OutboxRetention:   config.Duration(168 * time.Hour),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, cfg, discard()) }()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "publish work dir") {
			t.Fatalf("runServe = %v, want a boot refusal naming the publish work dir", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("runServe went on serving with a work dir it cannot create: a misconfiguration " +
			"has to be caught at boot, not at the first publication")
	}
}

// The shape the sibling above cannot reach, and the one a real deployment
// actually presents. os.MkdirAll returns nil on a directory that ALREADY
// exists, whatever its owner or mode — and the packaged default
// /var/lib/caesar/publish is exactly the path a deploy pre-creates, under
// root, for a caesar process that is not root. Creation therefore answers
// nothing; only writing does.
func TestRunServe_RefusesToBootOnAPreExistingUnwritablePublishWorkDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the mode bits this test stages, so the refusal is unobservable as uid 0")
	}
	workDir := filepath.Join(t.TempDir(), "publish")
	// r-x: it exists, MkdirAll will succeed on it, and nothing may create a
	// file inside it. This is the whole point of the case.
	if err := os.Mkdir(workDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(workDir, 0o700) })
	// Guard the staging itself: if the platform ignored the mode, the boot
	// would succeed and this test would read as a passing guard rather than
	// an unstaged one.
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatalf("MkdirAll on the pre-existing dir = %v; this test only means something if it succeeds", err)
	}
	if f, err := os.CreateTemp(workDir, "staging-*"); err == nil {
		f.Close()
		t.Fatalf("%s is writable after chmod 0500: the unwritable case was never staged", workDir)
	}

	cfg := config.Config{}
	cfg.Server.Addr = "127.0.0.1:0"
	cfg.Server.ShutdownTimeout = config.Duration(5 * time.Second)
	cfg.Database.URL = testdb.NewURL(t)
	cfg.Security.MasterKey = "dev:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	cfg.Forge.PublishWorkDir = workDir
	cfg.Orchestrator = config.Orchestrator{
		Workers:           2,
		PollInterval:      config.Duration(50 * time.Millisecond),
		LeaseTTL:          config.Duration(30 * time.Second),
		HeartbeatInterval: config.Duration(10 * time.Second),
		OutboxRetention:   config.Duration(168 * time.Hour),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, cfg, discard()) }()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "publish work dir") {
			t.Fatalf("runServe = %v, want a boot refusal naming the publish work dir", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("runServe went on serving with a work dir that exists and cannot be written: " +
			"every publication will die at its checkout, and the first one is the worst " +
			"possible moment to learn it")
	}
}

func TestRunServe_RefusesToBootWithoutMasterKey(t *testing.T) {
	cfg := config.Config{}
	cfg.Server.Addr = "127.0.0.1:0"
	cfg.Server.ShutdownTimeout = config.Duration(5 * time.Second)
	cfg.Database.URL = testdb.NewURL(t)

	err := runServe(context.Background(), cfg, discard())
	if err == nil || !strings.Contains(err.Error(), "security.master_key") {
		t.Fatalf("runServe = %v, want error mentioning security.master_key", err)
	}
}

// TestProductionServeNeverOverridesTheExecutor pins the seam's blast radius:
// runServe — the only path cmd/caesar reaches — passes an EMPTY serveOptions,
// so no deployment can be talked into a fake executor or a foreign listener by
// any config it accepts.
//
// It reads the source rather than the values, and that is the whole point. The
// obvious spelling of this test is `var zero serveOptions; if zero.executor !=
// nil {...}`, which asserts that Go zeroes struct fields — true of every
// program ever compiled, and still green after someone changes runServe to
// pass serveOptions{executor: somethingElse}. That is the property at issue,
// so the assertion has to be about the call runServe actually makes.
//
// MEASURED: putting any field in that literal turns this red, and removing the
// delegation entirely turns it red for a different reason.
func TestProductionServeNeverOverridesTheExecutor(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "serve.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var body []ast.Stmt
	for _, d := range file.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "runServe" {
			body = fn.Body.List
		}
	}
	if len(body) != 1 {
		t.Fatalf("runServe has %d statements, want exactly one: a delegation to runServeWith. "+
			"Anything else means production boot has grown a second path this test cannot see", len(body))
	}
	ret, ok := body[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		t.Fatalf("runServe's only statement is %T, want `return runServeWith(...)`", body[0])
	}
	call, ok := ret.Results[0].(*ast.CallExpr)
	if !ok || len(call.Args) != 4 {
		t.Fatalf("runServe does not return a four-argument call; got %#v", ret.Results[0])
	}
	if fn, ok := call.Fun.(*ast.Ident); !ok || fn.Name != "runServeWith" {
		t.Fatalf("runServe delegates to %v, want runServeWith", call.Fun)
	}
	lit, ok := call.Args[3].(*ast.CompositeLit)
	if !ok {
		t.Fatalf("runServe's fourth argument is %T, want a serveOptions literal written at the "+
			"call site — a variable could carry anything", call.Args[3])
	}
	if id, ok := lit.Type.(*ast.Ident); !ok || id.Name != "serveOptions" {
		t.Fatalf("runServe passes a %v literal, want serveOptions", lit.Type)
	}
	if len(lit.Elts) != 0 {
		t.Fatalf("runServe passes serveOptions with %d field(s) set, want none: production must "+
			"select the configured executor and bind cfg.Server.Addr itself", len(lit.Elts))
	}
}

func TestRoutes_HealthzAgainstRealDatabase(t *testing.T) {
	pool := testdb.New(t)
	resp, body := getHealthzWithPinger(t, pool)
	if resp.StatusCode != 200 || body["db"] != "ok" {
		t.Fatalf("status=%d body=%v", resp.StatusCode, body)
	}
}
