/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package workdir

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUseAndResolve(t *testing.T) {
	dir := t.TempDir()
	w, err := Use(dir, "main")
	if err != nil {
		t.Fatalf("Use: %v", err)
	}

	abs, err := w.Resolve("foo/bar.txt")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if filepath.Dir(abs) != filepath.Join(dir, "foo") {
		t.Errorf("resolved path unexpected: %s", abs)
	}

	if _, err := w.Resolve("../escape"); err == nil {
		t.Error("expected escape to fail")
	}
}

func TestWriteAndReadFile(t *testing.T) {
	w, err := Use(t.TempDir(), "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFile("a/b.txt", "hello"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := w.ReadFile("a/b.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if got != "hello" {
		t.Errorf("got %q, want hello", got)
	}
}

func TestBash(t *testing.T) {
	w, err := Use(t.TempDir(), "main")
	if err != nil {
		t.Fatal(err)
	}
	stdout, _, code, err := w.Bash(context.Background(), "echo hi")
	if err != nil {
		t.Fatalf("Bash: %v", err)
	}
	if code != 0 || !strings.Contains(stdout, "hi") {
		t.Errorf("stdout=%q code=%d", stdout, code)
	}

	_, _, code, err = w.Bash(context.Background(), "exit 7")
	if err != nil {
		t.Fatalf("Bash exit 7: %v", err)
	}
	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
}

func TestCloseOnlyRemovesCloned(t *testing.T) {
	dir := t.TempDir()
	w, _ := Use(dir, "main")
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("Use-opened dir should not be removed, got %v", err)
	}
}
