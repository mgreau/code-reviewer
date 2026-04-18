/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

// Package workdir manages a local working directory rooted at a PR branch
// checkout. It replaces the Vercel Sandbox used by openreview with a plain
// filesystem that bash/read_file/write_file tools can operate on. Callers can
// either supply an existing checkout via Use or let the package clone the PR
// branch on demand via Clone.
package workdir

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrNoWorkdir is returned by tools when no workdir is available.
var ErrNoWorkdir = errors.New("no working directory configured")

// Workdir is an authenticated checkout that the agent can read, write, and
// commit to. Close() removes the directory only when it was cloned by Clone.
type Workdir struct {
	Root   string
	Branch string
	// cloned tracks whether Close should remove Root.
	cloned bool
}

// Use wraps an existing checkout at root without cloning or cleaning up.
func Use(root, branch string) (*Workdir, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workdir: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("stat workdir %s: %w", abs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workdir %s is not a directory", abs)
	}
	return &Workdir{Root: abs, Branch: branch}, nil
}

// Clone checks out repo@branch into a fresh temp directory using the supplied
// token. It mirrors the openreview create-sandbox step, using shallow depth=1
// for the same speed trade-off.
func Clone(ctx context.Context, repoFullName, branch, token string) (*Workdir, error) {
	root, err := os.MkdirTemp("", "code-reviewer-*")
	if err != nil {
		return nil, fmt.Errorf("mkdir tmp: %w", err)
	}

	url := fmt.Sprintf("https://x-access-token:%s@github.com/%s.git", token, repoFullName)
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth=1", "--branch", branch, url, root)
	// Silence the auth token on stderr; git only echoes it on failure.
	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("git clone %s@%s: %w: %s", repoFullName, branch, err, string(out))
	}
	return &Workdir{Root: root, Branch: branch, cloned: true}, nil
}

// Close removes the checkout if it was created by Clone.
func (w *Workdir) Close() error {
	if w == nil || !w.cloned {
		return nil
	}
	return os.RemoveAll(w.Root)
}

// Resolve makes path absolute and prevents escape outside Root. Returns the
// absolute path.
func (w *Workdir) Resolve(path string) (string, error) {
	if w == nil {
		return "", ErrNoWorkdir
	}
	if filepath.IsAbs(path) {
		rel, err := filepath.Rel(w.Root, path)
		if err != nil || strings.HasPrefix(rel, "..") {
			return "", fmt.Errorf("path %s escapes workdir", path)
		}
		return path, nil
	}
	abs := filepath.Join(w.Root, path)
	rel, err := filepath.Rel(w.Root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path %s escapes workdir", path)
	}
	return abs, nil
}

// ReadFile reads a file rooted in the workdir.
func (w *Workdir) ReadFile(path string) (string, error) {
	abs, err := w.Resolve(path)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// WriteFile writes content to path, creating parent directories as needed.
func (w *Workdir) WriteFile(path, content string) error {
	abs, err := w.Resolve(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, []byte(content), 0o644)
}

// Bash runs command via bash -c with Root as the cwd. A combined-output string
// plus exit code is returned so the tool response matches the openreview shape.
func (w *Workdir) Bash(ctx context.Context, command string) (stdout, stderr string, exitCode int, err error) {
	if w == nil {
		return "", "", -1, ErrNoWorkdir
	}
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = w.Root
	cmd.Env = os.Environ()

	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	if exitErr, ok := runErr.(*exec.ExitError); ok {
		return outBuf.String(), errBuf.String(), exitErr.ExitCode(), nil
	}
	if runErr != nil {
		return outBuf.String(), errBuf.String(), -1, runErr
	}
	return outBuf.String(), errBuf.String(), 0, nil
}

// HasUncommittedChanges returns true when git sees local modifications.
func (w *Workdir) HasUncommittedChanges(ctx context.Context) (bool, error) {
	out, _, _, err := w.Bash(ctx, "git status --porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// ConfigureGit sets user.name/email and primes the auth header so subsequent
// pushes use the provided token. Safe to call multiple times.
func (w *Workdir) ConfigureGit(ctx context.Context, token, name, email string) error {
	if name == "" {
		name = "code-reviewer[bot]"
	}
	if email == "" {
		email = "code-reviewer@users.noreply.github.com"
	}
	cmds := []string{
		fmt.Sprintf("git config user.name %q", name),
		fmt.Sprintf("git config user.email %q", email),
	}
	if token != "" {
		// Rewrite https://github.com/ to embed the token; mirrors the
		// openreview configure-git step without writing ~/.netrc.
		cmds = append(cmds,
			fmt.Sprintf("git config --local http.https://github.com/.extraheader %q", "AUTHORIZATION: basic "+basicAuth("x-access-token", token)),
		)
	}
	for _, c := range cmds {
		if _, stderr, code, err := w.Bash(ctx, c); err != nil || code != 0 {
			return fmt.Errorf("configure git (%s): code=%d %s %w", c, code, stderr, err)
		}
	}
	return nil
}

// CommitAndPush stages all changes, commits with message, and pushes to branch.
// Returns the commit SHA on success.
func (w *Workdir) CommitAndPush(ctx context.Context, message, branch string) (string, error) {
	if _, stderr, code, err := w.Bash(ctx, "git add -A"); err != nil || code != 0 {
		return "", fmt.Errorf("git add: %s %w", stderr, err)
	}
	if _, stderr, code, err := w.Bash(ctx, fmt.Sprintf("git commit --no-verify -m %q", message)); err != nil || code != 0 {
		return "", fmt.Errorf("git commit: %s %w", stderr, err)
	}
	pushCmd := "git push"
	if branch != "" {
		pushCmd = fmt.Sprintf("git push origin %s", branch)
	}
	if _, stderr, code, err := w.Bash(ctx, pushCmd); err != nil || code != 0 {
		return "", fmt.Errorf("git push: %s %w", stderr, err)
	}
	sha, _, _, err := w.Bash(ctx, "git rev-parse HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(sha), nil
}

func basicAuth(user, password string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + password))
}
