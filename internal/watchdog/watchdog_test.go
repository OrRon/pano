//go:build unix

package watchdog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/sysproxy"
)

// helperEnv names a file; when set, the test binary acts as the spawned
// watchdog child: it records its argv and session id there and exits.
const helperEnv = "PANO_WATCHDOG_TEST_HELPER"

func TestMain(m *testing.M) {
	if out := os.Getenv(helperEnv); out != "" {
		// Setsid makes the child a session and process-group leader.
		body := fmt.Sprintf("%s\npgid=%d pid=%d\n", strings.Join(os.Args[1:], " "), syscall.Getpgrp(), os.Getpid())
		if err := os.WriteFile(out, []byte(body), 0o600); err != nil {
			os.Exit(2)
		}
		fmt.Println("helper ran")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func startSleep(t *testing.T, secs string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", secs)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	return cmd
}

func TestWaitExitReturnsWhenProcessExits(t *testing.T) {
	cmd := startSleep(t, "0.2")
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := WaitExit(ctx, cmd.Process.Pid); err != nil {
		t.Fatalf("WaitExit: %v", err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("WaitExit took %v", took)
	}
	// The child exited but is still a zombie until reaped; kqueue must have
	// fired on its exit rather than on anything else.
	if err := cmd.Wait(); err != nil {
		t.Fatalf("sleep: %v", err)
	}
}

func TestWaitExitHonoursContext(t *testing.T) {
	cmd := startSleep(t, "10")
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- WaitExit(ctx, cmd.Process.Pid) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("WaitExit = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitExit did not return after cancellation")
	}
}

func TestWaitExitOnVanishedProcess(t *testing.T) {
	cmd := startSleep(t, "0")
	_ = cmd.Wait() // reaped: the pid is gone
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := WaitExit(ctx, cmd.Process.Pid); err != nil {
		t.Fatalf("WaitExit on reaped pid: %v", err)
	}
	if err := WaitExit(ctx, 0); err == nil {
		t.Fatal("WaitExit(0) succeeded")
	}
}

// fakeManager records RestoreStale calls.
type fakeManager struct {
	mu       sync.Mutex
	calls    int
	restored bool
	err      error
	path     string
}

func (f *fakeManager) Supported() bool { return true }
func (f *fakeManager) Enable(context.Context, string, int, []string) error {
	return errors.New("unexpected Enable")
}
func (f *fakeManager) Disable(context.Context) error { return errors.New("unexpected Disable") }
func (f *fakeManager) Status(context.Context, string, int) (api.SysProxy, error) {
	return api.SysProxy{}, errors.New("unexpected Status")
}

func (f *fakeManager) RestoreStale(ctx context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.restored, f.err
}

func installFake(t *testing.T, fm *fakeManager) {
	t.Helper()
	orig := newManager
	newManager = func(statePath string, _ *slog.Logger) sysproxy.Manager {
		fm.mu.Lock()
		fm.path = statePath
		fm.mu.Unlock()
		return fm
	}
	t.Cleanup(func() { newManager = orig })
}

func TestRunRestoresAfterExit(t *testing.T) {
	for _, restored := range []bool{true, false} {
		fm := &fakeManager{restored: restored}
		installFake(t, fm)
		cmd := startSleep(t, "0.2")
		state := filepath.Join(t.TempDir(), "sysproxy.json")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := Run(ctx, cmd.Process.Pid, state, slog.New(slog.DiscardHandler)); err != nil {
			t.Fatalf("Run: %v", err)
		}
		cancel()
		_ = cmd.Wait()
		if fm.calls != 1 || fm.path != state {
			t.Fatalf("RestoreStale calls=%d path=%q, want 1 call with %q", fm.calls, fm.path, state)
		}
	}
}

func TestRunPropagatesRestoreError(t *testing.T) {
	fm := &fakeManager{err: errors.New("boom")}
	installFake(t, fm)
	cmd := startSleep(t, "0")
	_ = cmd.Wait()
	err := Run(context.Background(), cmd.Process.Pid, "/nonexistent/state.json", nil)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Run = %v, want restore error", err)
	}
}

func TestRunStopsOnCancelWithoutRestoring(t *testing.T) {
	fm := &fakeManager{}
	installFake(t, fm)
	cmd := startSleep(t, "10")
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cmd.Process.Pid, "/nonexistent/state.json", nil) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	if fm.calls != 0 {
		t.Fatal("RestoreStale called although the daemon is still running")
	}
}

func TestRunUsesRealManagerWithoutState(t *testing.T) {
	// No state file: the real sysproxy manager has nothing to restore and
	// must not touch the system.
	cmd := startSleep(t, "0")
	_ = cmd.Wait()
	state := filepath.Join(t.TempDir(), "sysproxy.json")
	if err := Run(context.Background(), cmd.Process.Pid, state, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestSpawn(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	logPath := filepath.Join(dir, "watchdog.log")
	state := filepath.Join(dir, "sysproxy.json")
	t.Setenv(helperEnv, record)

	child, err := Spawn(self, os.Getpid(), state, logPath)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if child <= 0 {
		t.Fatalf("child pid = %d", child)
	}

	var body []byte
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if body, err = os.ReadFile(record); err == nil && strings.Count(string(body), "\n") >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("helper did not run: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	wantArgs := "_watchdog --pid " + strconv.Itoa(os.Getpid()) + " --state " + state
	if lines[0] != wantArgs {
		t.Fatalf("child argv = %q, want %q", lines[0], wantArgs)
	}
	if want := fmt.Sprintf("pgid=%d pid=%d", child, child); lines[1] != want {
		t.Fatalf("child session = %q, want %q (own process group)", lines[1], want)
	}

	// stdout was redirected to the log file.
	for time.Now().Before(deadline) {
		if b, _ := os.ReadFile(logPath); strings.Contains(string(b), "helper ran") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("child output did not reach the log file")
}

func TestSpawnDiscardsOutputWithoutLog(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(t.TempDir(), "record")
	t.Setenv(helperEnv, record)
	if _, err := Spawn(self, os.Getpid(), "/tmp/state.json", ""); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(record); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("helper did not run")
}

func TestSpawnValidation(t *testing.T) {
	cases := []struct {
		self  string
		pid   int
		state string
		log   string
	}{
		{"", 1, "/s", ""},
		{"/bin/true", 0, "/s", ""},
		{"/bin/true", 1, "", ""},
		{filepath.Join(t.TempDir(), "missing"), 1, "/s", ""},
		{"/bin/true", 1, "/s", filepath.Join(t.TempDir(), "no", "such", "dir", "log")},
	}
	for _, c := range cases {
		if _, err := Spawn(c.self, c.pid, c.state, c.log); err == nil {
			t.Errorf("Spawn(%q, %d, %q, %q) succeeded", c.self, c.pid, c.state, c.log)
		}
	}
}
