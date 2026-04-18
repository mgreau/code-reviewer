/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

// Package skills implements a progressive skill system modeled after
// https://github.com/vercel-labs/openreview. Skills live under
// .agents/skills/<name>/SKILL.md with YAML frontmatter. Only the skill names
// and descriptions are surfaced to the agent up-front; full instructions are
// loaded on demand via the load_skill tool to keep the context focused.
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Skill is a discovered skill with its full body content.
type Skill struct {
	Name        string
	Description string
	Content     string
	Source      string // absolute path to SKILL.md
}

var (
	frontmatterRE = regexp.MustCompile(`(?s)^---\r?\n(.*?)\r?\n---\r?\n?`)
	nameRE        = regexp.MustCompile(`(?m)^name:\s*(.+)$`)
	descRE        = regexp.MustCompile(`(?m)^description:\s*(.+)$`)
)

// ParseFrontmatter extracts the name, description, and body from a SKILL.md
// file. Matches the openreview parser so the same skills work in both runtimes.
func ParseFrontmatter(raw string) (name, description, body string, err error) {
	m := frontmatterRE.FindStringSubmatchIndex(raw)
	if m == nil {
		return "", "", "", fmt.Errorf("no frontmatter found")
	}

	fm := raw[m[2]:m[3]]

	nameMatch := nameRE.FindStringSubmatch(fm)
	descMatch := descRE.FindStringSubmatch(fm)
	if len(nameMatch) < 2 || len(descMatch) < 2 {
		return "", "", "", fmt.Errorf("missing name or description in frontmatter")
	}

	body = strings.TrimSpace(raw[m[1]:])
	return strings.TrimSpace(nameMatch[1]), strings.TrimSpace(descMatch[1]), body, nil
}

// Discover scans one or more directories for skills. The first definition wins
// when multiple directories contain the same skill name; directories that do
// not exist are silently skipped.
func Discover(dirs ...string) ([]Skill, error) {
	seen := make(map[string]bool)
	var skills []Skill

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read skills dir %s: %w", dir, err)
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			skillFile := filepath.Join(dir, entry.Name(), "SKILL.md")
			raw, err := os.ReadFile(skillFile)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, fmt.Errorf("read skill %s: %w", skillFile, err)
			}
			name, desc, body, err := ParseFrontmatter(string(raw))
			if err != nil {
				// Skip malformed skills; fail-closed would be more hostile than useful.
				continue
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			skills = append(skills, Skill{
				Name:        name,
				Description: desc,
				Content:     body,
				Source:      skillFile,
			})
		}
	}

	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills, nil
}

// BuildPrompt renders the skill list section appended to the agent's system
// prompt. Returns an empty string when no skills are available.
func BuildPrompt(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Skills\n\n")
	b.WriteString("Use the `load_skill` tool to load a skill when the user's request would benefit from specialized instructions. Only the skill names and descriptions are shown here — load a skill to get the full instructions.\n\n")
	b.WriteString("Available skills:\n")
	for _, s := range skills {
		fmt.Fprintf(&b, "- **%s**: %s\n", s.Name, s.Description)
	}
	return b.String()
}

// Find returns the skill matching name (case-insensitive) or nil.
func Find(skills []Skill, name string) *Skill {
	for i := range skills {
		if strings.EqualFold(skills[i].Name, name) {
			return &skills[i]
		}
	}
	return nil
}
