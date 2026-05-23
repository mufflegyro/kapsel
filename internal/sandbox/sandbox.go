package sandbox

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const defaultKillGrace = 5 * time.Second

type Kind string

const (
	KindGeneric Kind = "generic"
	KindYTDLP   Kind = "yt-dlp"
	KindFFMPEG  Kind = "ffmpeg"
)

type NetworkPolicy string

const (
	NetworkDefault NetworkPolicy = "default"
	NetworkAllow   NetworkPolicy = "allow"
	NetworkDeny    NetworkPolicy = "deny"
)

type PathKind string

const (
	PathLiteral PathKind = "literal"
	PathSubtree PathKind = "subtree"
)

type PathGrant struct {
	Path string
	Kind PathKind
}

type Access struct {
	ReadOnly  []PathGrant
	ReadWrite []PathGrant
	Deny      []PathGrant
}

type Spec struct {
	Name          string
	Args          []string
	Kind          Kind
	Dir           string
	Env           []string
	ScratchParent string
	KillGrace     time.Duration
	Access        Access
	Network       NetworkPolicy
}

type IO struct {
	Stdout io.Writer
	Stderr io.Writer
}

type Capabilities struct {
	EnvIsolation        bool
	ProcessTreeCleanup  bool
	FilesystemIsolation bool
	NetworkIsolation    bool
}

type Backend interface {
	Name() string
	Capabilities() Capabilities
	Run(context.Context, Spec, IO) error
}

type BasicBackend struct{}

func (BasicBackend) Name() string {
	return "basic"
}

func (BasicBackend) Capabilities() Capabilities {
	return Capabilities{EnvIsolation: true, ProcessTreeCleanup: processTreeCleanupAvailable}
}

func (BasicBackend) Run(ctx context.Context, spec Spec, stdio IO) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	scratch, err := createScratch(spec.ScratchParent)
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)

	dir := spec.Dir
	if strings.TrimSpace(dir) == "" {
		dir = scratch
	}
	env := spec.Env
	if env == nil {
		env = DefaultEnv(scratch)
	}
	cmd := exec.Command(spec.Name, spec.Args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = stdio.Stdout
	cmd.Stderr = stdio.Stderr
	configureProcessTree(cmd)

	if err := ctx.Err(); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	processTree := startedProcessTree(cmd)
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()

	select {
	case err := <-wait:
		if ctxErr := ctx.Err(); ctxErr != nil {
			killProcessTree(processTree)
			return ctxErr
		}
		return err
	case <-ctx.Done():
		requestProcessTreeShutdown(processTree)
		killGrace := spec.KillGrace
		if killGrace <= 0 {
			killGrace = defaultKillGrace
		}
		timer := time.NewTimer(killGrace)
		defer timer.Stop()
		select {
		case <-wait:
			killProcessTree(processTree)
			return ctx.Err()
		case <-timer.C:
			killProcessTree(processTree)
			<-wait
			return ctx.Err()
		}
	}
}

func DefaultEnv(scratch string) []string {
	env := []string{}
	for _, pair := range os.Environ() {
		key, _, _ := strings.Cut(pair, "=")
		if allowedParentEnv(key) {
			env = append(env, pair)
		}
	}
	env = setEnv(env, "PATH", defaultPath(os.Getenv("PATH")))
	env = setEnv(env, "HOME", filepath.Join(scratch, "home"))
	env = setEnv(env, "TMPDIR", filepath.Join(scratch, "tmp"))
	env = setEnv(env, "XDG_CACHE_HOME", filepath.Join(scratch, "cache"))
	env = setEnv(env, "XDG_CONFIG_HOME", filepath.Join(scratch, "config"))
	env = setEnv(env, "XDG_DATA_HOME", filepath.Join(scratch, "data"))
	env = setEnv(env, "PYTHONNOUSERSITE", "1")
	env = setEnv(env, "PYTHONDONTWRITEBYTECODE", "1")

	return env
}

func createScratch(parent string) (string, error) {
	if strings.TrimSpace(parent) == "" {
		parent = os.TempDir()
	}
	scratch, err := os.MkdirTemp(parent, "kapsel-sandbox-*")
	if err != nil {
		return "", err
	}
	for _, name := range []string{"home", "tmp", "cache", "config", "data"} {
		if err := os.MkdirAll(filepath.Join(scratch, name), 0o700); err != nil {
			_ = os.RemoveAll(scratch)
			return "", err
		}
	}

	return scratch, nil
}

func allowedParentEnv(key string) bool {
	switch key {
	case "PATH", "LANG", "LC_ALL", "LC_CTYPE", "TZ", "SSL_CERT_FILE", "SSL_CERT_DIR":
		return true
	default:
		return false
	}
}

func setEnv(env []string, key string, value string) []string {
	prefix := key + "="
	for index, pair := range env {
		if strings.HasPrefix(pair, prefix) {
			env[index] = prefix + value
			return env
		}
	}

	return append(env, prefix+value)
}

func defaultPath(value string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}

	return "/usr/local/bin:/usr/bin:/bin"
}
