package download

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"kapsel/internal/redact"
)

const (
	MinimumTestedYTDLPVersion = "2026.03.17"
	maxDiagnosticLength       = 1200
	maxDiagnosticOutput       = 64 * 1024
	ytdlpCheckTimeout         = 2 * time.Second
)

type YTDLPStatus struct {
	Path                 string `json:"path"`
	Available            bool   `json:"available"`
	Version              string `json:"version,omitempty"`
	MinimumTestedVersion string `json:"minimum_tested_version"`
	Error                string `json:"error,omitempty"`
}

func CheckYTDLP(ctx context.Context, path string, runner Runner) YTDLPStatus {
	return checkYTDLP(ctx, path, runner, ytdlpCheckTimeout)
}

func checkYTDLP(ctx context.Context, path string, runner Runner, timeout time.Duration) YTDLPStatus {
	if path == "" {
		path = defaultYTDLPPath
	}
	if runner == nil {
		runner = ExecRunner{}
	}

	status := YTDLPStatus{Path: path, MinimumTestedVersion: MinimumTestedYTDLPVersion}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	command := Command{Name: path, Args: []string{"--version"}, MaxStdoutBytes: maxDiagnosticOutput}
	output, err := runner.Run(checkCtx, command)
	if err != nil {
		if errors.Is(checkCtx.Err(), context.DeadlineExceeded) {
			status.Error = fmt.Sprintf("yt-dlp command timed out at %q", command.Name)
			return status
		}
		status.Error = sanitizeDiagnosticText(ytdlpCommandError(command, output, err).Error())
		return status
	}

	version := firstNonEmptyLine(output)
	if version == "" {
		status.Error = "yt-dlp version check returned empty output"
		return status
	}

	status.Available = true
	status.Version = version
	return status
}

func ytdlpCommandError(command Command, output []byte, err error) error {
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("yt-dlp unavailable at %q: executable not found", command.Name)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("yt-dlp command timed out at %q", command.Name)
	}

	detail := strings.TrimSpace(string(output))
	if detail == "" {
		detail = err.Error()
	} else if err.Error() != "" {
		detail = detail + " (" + err.Error() + ")"
	}
	detail = sanitizeStoredDiagnosticText(detail)
	if detail == "" {
		detail = "no details reported"
	}

	return fmt.Errorf("yt-dlp command failed at %q: %s", command.Name, detail)
}

func firstJSONObjectEnd(data []byte) int {
	if len(data) == 0 || data[0] != '{' {
		return -1
	}
	depth := 0
	inString := false
	escaped := false
	for index, value := range data {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch value {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch value {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return index + 1
			}
		}
	}

	return -1
}

func firstNonEmptyLine(output []byte) string {
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}

	return ""
}

func SanitizeDiagnosticText(text string) string {
	return sanitizeDiagnosticText(text)
}

func sanitizeStoredDiagnosticText(text string) string {
	return redact.Text(text, 0)
}

func sanitizeDiagnosticText(text string) string {
	return redact.Text(text, maxDiagnosticLength)
}
