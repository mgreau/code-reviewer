/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package reviewer

import "testing"

func TestExtractSuggestionBlock(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
		ok   bool
	}{
		{
			name: "simple",
			body: "[#1] **error**: fix it\n\n```suggestion\nif err != nil {\n\treturn err\n}\n```",
			want: "if err != nil {\n\treturn err\n}",
			ok:   true,
		},
		{
			name: "empty suggestion",
			body: "**note**: drop the line\n\n```suggestion\n```",
			want: "",
			ok:   true,
		},
		{
			name: "no suggestion block",
			body: "just a comment, no fence",
			ok:   false,
		},
		{
			name: "unclosed fence",
			body: "```suggestion\nstill typing",
			ok:   false,
		},
		{
			name: "suggestion after other fences",
			body: "```go\nold code\n```\n\n```suggestion\nnew code\n```",
			want: "new code",
			ok:   true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := extractSuggestionBlock(c.body)
			if ok != c.ok {
				t.Fatalf("ok=%v want %v", ok, c.ok)
			}
			if ok && got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestBuildApplyCommitMessage(t *testing.T) {
	cases := []struct {
		name    string
		applied []int
		want    string
	}{
		{"empty", nil, "code-reviewer: apply suggestions"},
		{"single", []int{3}, "code-reviewer: apply suggestions #3"},
		{"multiple", []int{1, 4, 7}, "code-reviewer: apply suggestions #1, #4, #7"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildApplyCommitMessage(c.applied)
			if got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}
