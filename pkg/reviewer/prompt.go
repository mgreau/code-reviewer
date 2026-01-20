/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package reviewer

import "chainguard.dev/driftless/pkg/promptbuilder"

// ReviewPrompt is the main prompt template for code review.
var ReviewPrompt = promptbuilder.MustNewPrompt(`You are an expert code reviewer. Review the following PR changes.

## PR Information
{{pr_info}}

## Changed Files
{{files}}

## Diff
{{diff}}

## Instructions
1. Review the code for general quality issues:
   - Bugs and logic errors
   - Code style and best practices
   - Missing error handling
   - Readability and maintainability
   - Potential edge cases
   - Security vulnerabilities

2. For each issue found, provide:
   - File path and line numbers
   - Clear explanation of the problem
   - Concrete suggestion with code

3. Be constructive and specific. Only flag real issues that matter.
   - Focus on bugs, security issues, and logic errors first
   - Then consider style and best practices
   - Avoid nitpicking or suggesting changes for change's sake

4. Use the read_file tool if you need to see the full content of a file for context.

5. When finished, submit your review using the submit_result tool with:
   - A summary of your findings
   - A list of suggestions with file, line numbers, severity, message, and suggested fix
   - Whether the PR is approved (no errors found)`)

// PRInfo contains the PR metadata for XML binding.
type PRInfo struct {
	Repo        string `xml:"repository"`
	Title       string `xml:"title"`
	Description string `xml:"description"`
}

// FileList wraps the file listing for XML binding.
type FileList struct {
	Content string `xml:",cdata"`
}

// DiffContent wraps the diff for XML binding.
type DiffContent struct {
	Content string `xml:",cdata"`
}

// Bind implements promptbuilder.Bindable for ReviewRequest.
func (r *ReviewRequest) Bind(prompt *promptbuilder.Prompt) (*promptbuilder.Prompt, error) {
	p, err := prompt.BindXML("pr_info", PRInfo{
		Repo:        r.Repo,
		Title:       r.Title,
		Description: r.Description,
	})
	if err != nil {
		return nil, err
	}

	p, err = p.BindXML("files", FileList{Content: r.Files})
	if err != nil {
		return nil, err
	}

	p, err = p.BindXML("diff", DiffContent{Content: r.Diff})
	if err != nil {
		return nil, err
	}

	return p, nil
}
