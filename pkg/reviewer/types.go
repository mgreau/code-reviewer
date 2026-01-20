/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package reviewer

// ReviewResult contains the structured output from a code review.
type ReviewResult struct {
	Summary     string           `json:"summary" jsonschema:"description=Overall review summary highlighting key findings"`
	Suggestions []CodeSuggestion `json:"suggestions" jsonschema:"description=List of code suggestions and issues found"`
	Approved    bool             `json:"approved" jsonschema:"description=Whether the PR is approved for merge"`
}

// CodeSuggestion represents a single code review suggestion.
type CodeSuggestion struct {
	File       string `json:"file" jsonschema:"description=File path relative to repository root"`
	LineStart  int    `json:"line_start" jsonschema:"description=Starting line number of the issue"`
	LineEnd    int    `json:"line_end" jsonschema:"description=Ending line number of the issue"`
	Severity   string `json:"severity" jsonschema:"description=Severity level: error, warning, or info"`
	Message    string `json:"message" jsonschema:"description=Clear explanation of the issue found"`
	Suggestion string `json:"suggestion" jsonschema:"description=Suggested code fix or improvement"`
}

// ReviewRequest contains all the data needed to review a PR.
type ReviewRequest struct {
	Repo        string `json:"repo"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Files       string `json:"files"`
	Diff        string `json:"diff"`
}
