/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseFrontmatter(t *testing.T) {
	raw := `---
name: my-skill
description: A helpful skill.
---

# My Skill

Body here.`

	name, desc, body, err := ParseFrontmatter(raw)
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}
	if name != "my-skill" {
		t.Errorf("name = %q, want my-skill", name)
	}
	if desc != "A helpful skill." {
		t.Errorf("description = %q", desc)
	}
	if !strings.Contains(body, "# My Skill") {
		t.Errorf("body missing heading: %q", body)
	}
}

func TestParseFrontmatterMissing(t *testing.T) {
	if _, _, _, err := ParseFrontmatter("# No frontmatter"); err == nil {
		t.Error("expected error for missing frontmatter")
	}
}

func TestDiscover(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "example")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(
		"---\nname: example\ndescription: Example skill.\n---\n\nContent.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	skills, err := Discover(dir, "/does/not/exist")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(skills) != 1 || skills[0].Name != "example" {
		t.Fatalf("unexpected skills: %+v", skills)
	}
}

func TestBuildPrompt(t *testing.T) {
	if BuildPrompt(nil) != "" {
		t.Error("expected empty prompt for no skills")
	}
	got := BuildPrompt([]Skill{{Name: "foo", Description: "does foo"}})
	if !strings.Contains(got, "**foo**: does foo") {
		t.Errorf("BuildPrompt missing skill line: %q", got)
	}
}

func TestFind(t *testing.T) {
	skills := []Skill{{Name: "Alpha"}, {Name: "beta"}}
	if Find(skills, "ALPHA") == nil {
		t.Error("expected case-insensitive match")
	}
	if Find(skills, "missing") != nil {
		t.Error("expected nil for missing")
	}
}
