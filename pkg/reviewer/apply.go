/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package reviewer

import (
	"context"
	"fmt"
	"sort"
	"strings"

	gh "github.com/google/go-github/v68/github"
)

// ApplyResult summarizes what an ApplySuggestions call did.
type ApplyResult struct {
	// Applied is the list of 1-based suggestion indices actually written.
	Applied []int
	// Missing is the list of requested indices that did not resolve to a bot
	// review comment (out of range, no ```suggestion``` block, etc.).
	Missing []int
	// PushedSHA is the commit SHA pushed to the PR branch, or "" if nothing
	// changed.
	PushedSHA string
	// ChangedFiles is the set of files edited.
	ChangedFiles []string
}

// ApplySuggestions applies suggestion(s) from the bot's inline review comments
// back to files in the workdir, then commits and pushes.
//
// nums is a 1-based list of indices matching the `[#N]` markers the reviewer
// prefixes to each inline comment body. When all is true, every bot suggestion
// on the PR is applied.
//
// Requires a configured workdir, GitHub client, and bot login.
func (r *Reviewer) ApplySuggestions(ctx context.Context, owner, repo string, prNumber int, nums []int, all bool) (*ApplyResult, error) {
	if r.workdir == nil {
		return nil, fmt.Errorf("apply requires a workdir (run serve with -clone)")
	}
	if r.github == nil {
		return nil, fmt.Errorf("apply requires a GitHub client")
	}
	if r.botLogin == "" {
		return nil, fmt.Errorf("apply requires bot login to be set")
	}

	comments, err := r.github.ListPullRequestReviewComments(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("list review comments: %w", err)
	}

	// Filter to bot-authored comments, preserving creation order so that
	// indexing matches the [#N] numbering used in PostReview.
	var bot []*gh.PullRequestComment
	for _, c := range comments {
		if strings.EqualFold(c.GetUser().GetLogin(), r.botLogin) {
			bot = append(bot, c)
		}
	}

	var selected []indexedComment
	var missing []int
	if all {
		for i, c := range bot {
			selected = append(selected, indexedComment{idx: i + 1, comment: c})
		}
	} else {
		for _, n := range nums {
			if n < 1 || n > len(bot) {
				missing = append(missing, n)
				continue
			}
			selected = append(selected, indexedComment{idx: n, comment: bot[n-1]})
		}
	}

	// Group resolved edits by file, keep track of which indices actually
	// carried a ```suggestion``` block.
	byFile := map[string][]edit{}
	var applied []int
	for _, sel := range selected {
		repl, ok := extractSuggestionBlock(sel.comment.GetBody())
		if !ok {
			missing = append(missing, sel.idx)
			continue
		}
		path := sel.comment.GetPath()
		end := sel.comment.GetLine()
		if end == 0 {
			end = sel.comment.GetOriginalLine()
		}
		start := sel.comment.GetStartLine()
		if start == 0 {
			start = end
		}
		if path == "" || end == 0 {
			missing = append(missing, sel.idx)
			continue
		}
		byFile[path] = append(byFile[path], edit{start: start, end: end, replacement: repl})
		applied = append(applied, sel.idx)
	}

	// Apply edits per file, highest line first so earlier edits keep valid
	// line numbers.
	var changedFiles []string
	for path, edits := range byFile {
		sort.Slice(edits, func(i, j int) bool { return edits[i].end > edits[j].end })
		content, err := r.workdir.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		lines := strings.Split(content, "\n")
		for _, e := range edits {
			if e.start < 1 || e.end > len(lines) || e.start > e.end {
				continue
			}
			replLines := strings.Split(e.replacement, "\n")
			next := make([]string, 0, len(lines)-(e.end-e.start+1)+len(replLines))
			next = append(next, lines[:e.start-1]...)
			next = append(next, replLines...)
			next = append(next, lines[e.end:]...)
			lines = next
		}
		if err := r.workdir.WriteFile(path, strings.Join(lines, "\n")); err != nil {
			return nil, fmt.Errorf("write %s: %w", path, err)
		}
		changedFiles = append(changedFiles, path)
	}

	result := &ApplyResult{Applied: applied, Missing: missing, ChangedFiles: changedFiles}

	changed, err := r.workdir.HasUncommittedChanges(ctx)
	if err != nil {
		return nil, fmt.Errorf("check uncommitted: %w", err)
	}
	if !changed {
		return result, nil
	}

	msg := buildApplyCommitMessage(applied)
	sha, err := r.workdir.CommitAndPush(ctx, msg, r.workdir.Branch)
	if err != nil {
		return nil, fmt.Errorf("commit+push: %w", err)
	}
	result.PushedSHA = sha
	return result, nil
}

type indexedComment struct {
	idx     int
	comment *gh.PullRequestComment
}

type edit struct {
	start       int
	end         int
	replacement string
}

// extractSuggestionBlock returns the contents of the first ```suggestion ... ```
// fence in body. The trailing newline inside the fence is stripped.
func extractSuggestionBlock(body string) (string, bool) {
	const fence = "```suggestion"
	i := strings.Index(body, fence)
	if i < 0 {
		return "", false
	}
	rest := body[i+len(fence):]
	rest = strings.TrimPrefix(rest, "\n")
	j := strings.Index(rest, "```")
	if j < 0 {
		return "", false
	}
	content := strings.TrimSuffix(rest[:j], "\n")
	return content, true
}

func buildApplyCommitMessage(applied []int) string {
	if len(applied) == 0 {
		return "code-reviewer: apply suggestions"
	}
	parts := make([]string, len(applied))
	for i, n := range applied {
		parts[i] = fmt.Sprintf("#%d", n)
	}
	return fmt.Sprintf("code-reviewer: apply suggestions %s", strings.Join(parts, ", "))
}
