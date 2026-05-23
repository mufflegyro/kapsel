package sandbox

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBasicBackendMinimizesEnvironmentAndSetsScratch(t *testing.T) {
	t.Setenv("KAPSEL_SESSION_SECRET", "top-secret")
	t.Setenv("SHOULD_NOT_LEAK", "top-secret")
	t.Setenv("LC_SECRET", "top-secret")

	var stdout bytes.Buffer
	backend := BasicBackend{}
	err := backend.Run(context.Background(), Spec{
		Name: os.Args[0],
		Args: []string{"-test.run=TestSandboxHelper", "--", "env"},
	}, IO{Stdout: &stdout})
	if err != nil {
		t.Fatal(err)
	}
	env := stdout.String()
	for _, unexpected := range []string{"KAPSEL_SESSION_SECRET", "SHOULD_NOT_LEAK", "LC_SECRET", "top-secret"} {
		if strings.Contains(env, unexpected) {
			t.Fatalf("expected sandbox env not to contain %q, got %s", unexpected, env)
		}
	}
	for _, expected := range []string{"PATH=", "TMPDIR=", "XDG_CACHE_HOME=", "XDG_CONFIG_HOME=", "PYTHONNOUSERSITE=1"} {
		if !strings.Contains(env, expected) {
			t.Fatalf("expected sandbox env to contain %q, got %s", expected, env)
		}
	}
}

func TestBasicBackendUsesConfiguredWorkingDirectory(t *testing.T) {
	t.Parallel()

	workdir := t.TempDir()
	var stdout bytes.Buffer
	backend := BasicBackend{}
	if err := backend.Run(context.Background(), Spec{
		Name: os.Args[0],
		Args: []string{"-test.run=TestSandboxHelper", "--", "cwd"},
		Dir:  workdir,
	}, IO{Stdout: &stdout}); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(workdir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := filepath.EvalSymlinks(strings.TrimSpace(stdout.String()))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("expected cwd %q, got %q", want, got)
	}
}

func TestBasicBackendCancellationKillsProcessTree(t *testing.T) {
	t.Parallel()

	backend := BasicBackend{}
	if !backend.Capabilities().ProcessTreeCleanup {
		t.Skip("process-tree cleanup is not available on this platform")
	}
	dir := t.TempDir()
	readyPath := filepath.Join(dir, "ready")
	markerPath := filepath.Join(dir, "survived")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- backend.Run(ctx, Spec{
			Name:      os.Args[0],
			Args:      []string{"-test.run=TestSandboxHelper", "--", "spawn-marker", readyPath, markerPath},
			KillGrace: 100 * time.Millisecond,
		}, IO{})
	}()
	waitForSandboxFile(t, readyPath, time.Second)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected canceled error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("sandboxed process did not exit after cancellation")
	}
	time.Sleep(700 * time.Millisecond)
	if _, err := os.Stat(markerPath); err == nil {
		t.Fatal("expected child process to be killed before writing marker")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestBasicBackendCancellationKillsGrandchildAfterParentExits(t *testing.T) {
	t.Parallel()

	backend := BasicBackend{}
	if !backend.Capabilities().ProcessTreeCleanup {
		t.Skip("process-tree cleanup is not available on this platform")
	}
	dir := t.TempDir()
	childReadyPath := filepath.Join(dir, "child-ready")
	readyPath := filepath.Join(dir, "ready")
	markerPath := filepath.Join(dir, "survived")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- backend.Run(ctx, Spec{
			Name:      os.Args[0],
			Args:      []string{"-test.run=TestSandboxHelper", "--", "spawn-ignoring-marker", childReadyPath, readyPath, markerPath},
			KillGrace: 100 * time.Millisecond,
		}, IO{})
	}()
	waitForSandboxFile(t, readyPath, time.Second)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected canceled error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("sandboxed process did not exit after cancellation")
	}
	time.Sleep(700 * time.Millisecond)
	if _, err := os.Stat(markerPath); err == nil {
		t.Fatal("expected grandchild process to be killed before writing marker")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestBasicBackendDoesNotStartPreCanceledCommand(t *testing.T) {
	t.Parallel()

	markerPath := filepath.Join(t.TempDir(), "started")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := (BasicBackend{}).Run(ctx, Spec{
		Name: os.Args[0],
		Args: []string{"-test.run=TestSandboxHelper", "--", "write-marker", markerPath},
	}, IO{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled error, got %v", err)
	}
	if _, err := os.Stat(markerPath); err == nil {
		t.Fatal("expected pre-canceled command not to start")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestSandboxHelper(t *testing.T) {
	fields := sandboxHelperArgs()
	if len(fields) == 0 {
		return
	}
	switch fields[0] {
	case "env":
		_, _ = os.Stdout.WriteString(strings.Join(os.Environ(), "\n"))
		os.Exit(0)
	case "cwd":
		cwd, err := os.Getwd()
		if err != nil {
			_, _ = os.Stderr.WriteString(err.Error())
			os.Exit(1)
		}
		_, _ = os.Stdout.WriteString(cwd)
		os.Exit(0)
	}
	if len(fields) >= 3 && fields[0] == "spawn-marker" {
		readyPath := fields[1]
		markerPath := fields[2]
		child := exec.Command(os.Args[0], "-test.run=TestSandboxHelper", "--", "child-marker", markerPath)
		if err := child.Start(); err != nil {
			_, _ = os.Stderr.WriteString(err.Error())
			os.Exit(1)
		}
		if err := os.WriteFile(readyPath, []byte("ready"), 0o644); err != nil {
			_, _ = os.Stderr.WriteString(err.Error())
			os.Exit(1)
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	if len(fields) >= 2 && fields[0] == "child-marker" {
		markerPath := fields[1]
		time.Sleep(500 * time.Millisecond)
		_ = os.WriteFile(markerPath, []byte("survived"), 0o644)
		os.Exit(0)
	}
	if len(fields) >= 4 && fields[0] == "spawn-ignoring-marker" {
		childReadyPath := fields[1]
		readyPath := fields[2]
		markerPath := fields[3]
		child := exec.Command(os.Args[0], "-test.run=TestSandboxHelper", "--", "term-ignoring-marker", childReadyPath, markerPath)
		if err := child.Start(); err != nil {
			_, _ = os.Stderr.WriteString(err.Error())
			os.Exit(1)
		}
		if !waitForSandboxHelperFile(childReadyPath, time.Second) {
			_, _ = os.Stderr.WriteString("child marker process did not become ready")
			os.Exit(1)
		}
		if err := os.WriteFile(readyPath, []byte("ready"), 0o644); err != nil {
			_, _ = os.Stderr.WriteString(err.Error())
			os.Exit(1)
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	if len(fields) >= 3 && fields[0] == "term-ignoring-marker" {
		childReadyPath := fields[1]
		markerPath := fields[2]
		signal.Ignore(syscall.SIGTERM)
		if err := os.WriteFile(childReadyPath, []byte("ready"), 0o644); err != nil {
			_, _ = os.Stderr.WriteString(err.Error())
			os.Exit(1)
		}
		time.Sleep(500 * time.Millisecond)
		_ = os.WriteFile(markerPath, []byte("survived"), 0o644)
		os.Exit(0)
	}
	if len(fields) >= 2 && fields[0] == "write-marker" {
		markerPath := fields[1]
		_ = os.WriteFile(markerPath, []byte("started"), 0o644)
		os.Exit(0)
	}
}

func sandboxHelperArgs() []string {
	for index, arg := range os.Args {
		if arg == "--" && index+1 < len(os.Args) {
			return os.Args[index+1:]
		}
	}

	return nil
}

func waitForSandboxFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	if !waitForSandboxHelperFile(path, timeout) {
		t.Fatalf("timed out waiting for %s", path)
	}
}

func waitForSandboxHelperFile(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}

	return false
}
