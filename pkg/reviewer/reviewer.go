/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package reviewer

import (
	"context"
	"fmt"
	"strings"

	"chainguard.dev/driftless/pkg/evals"
	"chainguard.dev/driftless/pkg/executor/claudeexecutor"
	"chainguard.dev/driftless/pkg/executor/googleexecutor"
	"chainguard.dev/driftless/pkg/submitresult"
	"chainguard.dev/driftless/pkg/toolcall/claudetool"
	"chainguard.dev/driftless/pkg/toolcall/googletool"
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
	"github.com/anthropics/anthropic-sdk-go/vertex"
	ghclient "github.com/example/code-reviewer/pkg/github"
	gh "github.com/google/go-github/v68/github"
	"google.golang.org/genai"
)

// Reviewer orchestrates PR code reviews using AI.
type Reviewer struct {
	github     *ghclient.Client
	claudeExec claudeexecutor.Interface[*ReviewRequest, *ReviewResult]
	googleExec googleexecutor.Interface[*ReviewRequest, *ReviewResult]
	provider   string
}

// NewWithClaude creates a new Reviewer using Claude via Vertex AI.
func NewWithClaude(ctx context.Context, projectID, location string) (*Reviewer, error) {
	client := anthropic.NewClient(
		vertex.WithGoogleAuth(ctx, location, projectID),
	)

	exec, err := claudeexecutor.New[*ReviewRequest, *ReviewResult](
		client,
		ReviewPrompt,
		claudeexecutor.WithModel[*ReviewRequest, *ReviewResult]("claude-opus-4-5@20251101"),
		claudeexecutor.WithMaxTokens[*ReviewRequest, *ReviewResult](16000),
		claudeexecutor.WithTemperature[*ReviewRequest, *ReviewResult](0.1),
		claudeexecutor.WithSubmitResultProvider[*ReviewRequest, *ReviewResult](
			submitresult.ClaudeToolForResponse[*ReviewResult],
		),
	)
	if err != nil {
		return nil, fmt.Errorf("create claude executor: %w", err)
	}

	return &Reviewer{
		claudeExec: exec,
		provider:   "claude",
	}, nil
}

// NewWithGemini creates a new Reviewer using Gemini via Vertex AI.
func NewWithGemini(ctx context.Context, projectID, location string) (*Reviewer, error) {
	googleClient, err := genai.NewClient(ctx, &genai.ClientConfig{
		Project:  projectID,
		Location: location,
		Backend:  genai.BackendVertexAI,
	})
	if err != nil {
		return nil, fmt.Errorf("create vertex AI client: %w", err)
	}

	exec, err := googleexecutor.New[*ReviewRequest, *ReviewResult](
		googleClient,
		ReviewPrompt,
		googleexecutor.WithModel[*ReviewRequest, *ReviewResult]("gemini-2.5-flash"),
		googleexecutor.WithMaxOutputTokens[*ReviewRequest, *ReviewResult](16000),
		googleexecutor.WithTemperature[*ReviewRequest, *ReviewResult](0.1),
		googleexecutor.WithSubmitResultProvider[*ReviewRequest, *ReviewResult](
			submitresult.GoogleToolForResponse[*ReviewResult],
		),
	)
	if err != nil {
		return nil, fmt.Errorf("create gemini executor: %w", err)
	}

	return &Reviewer{
		googleExec: exec,
		provider:   "gemini",
	}, nil
}

// SetGitHub sets the GitHub client for the reviewer.
func (r *Reviewer) SetGitHub(client *ghclient.Client) {
	r.github = client
}

// Review performs a code review on the specified PR.
func (r *Reviewer) Review(ctx context.Context, owner, repo string, prNumber int) (*ReviewResult, error) {
	if r.github == nil {
		return nil, fmt.Errorf("GitHub client not set")
	}

	// Fetch PR metadata
	pr, err := r.github.GetPR(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("get PR: %w", err)
	}

	// Fetch PR diff
	diff, err := r.github.GetPRDiff(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("get PR diff: %w", err)
	}

	// Fetch changed files
	files, err := r.github.GetPRFiles(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("get PR files: %w", err)
	}

	// Build the request
	request := &ReviewRequest{
		Repo:        fmt.Sprintf("%s/%s", owner, repo),
		Title:       pr.GetTitle(),
		Description: pr.GetBody(),
		Files:       formatFiles(files),
		Diff:        diff,
	}

	sha := pr.GetHead().GetSHA()

	// Execute with the appropriate provider
	switch r.provider {
	case "claude":
		return r.reviewWithClaude(ctx, request, owner, repo, sha)
	case "gemini":
		return r.reviewWithGemini(ctx, request, owner, repo, sha)
	default:
		return nil, fmt.Errorf("unknown provider: %s", r.provider)
	}
}

func (r *Reviewer) reviewWithClaude(ctx context.Context, request *ReviewRequest, owner, repo, sha string) (*ReviewResult, error) {
	tools := map[string]claudeexecutor.ToolMetadata[*ReviewResult]{
		"read_file": r.claudeReadFileTool(owner, repo, sha),
	}
	return r.claudeExec.Execute(ctx, request, tools)
}

func (r *Reviewer) reviewWithGemini(ctx context.Context, request *ReviewRequest, owner, repo, sha string) (*ReviewResult, error) {
	tools := map[string]googleexecutor.ToolMetadata[*ReviewResult]{
		"read_file": r.geminiReadFileTool(owner, repo, sha),
	}
	return r.googleExec.Execute(ctx, request, tools)
}

// claudeReadFileTool creates a Claude tool for reading file contents.
func (r *Reviewer) claudeReadFileTool(owner, repo, sha string) claudeexecutor.ToolMetadata[*ReviewResult] {
	return claudeexecutor.ToolMetadata[*ReviewResult]{
		Definition: anthropic.ToolParam{
			Name:        "read_file",
			Description: anthropic.String("Read the full content of a file in the PR for additional context"),
			InputSchema: anthropic.ToolInputSchemaParam{
				Type: constant.Object("object"),
				Properties: map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "File path relative to repository root",
					},
				},
				Required: []string{"path"},
			},
		},
		Handler: func(ctx context.Context, toolUse anthropic.ToolUseBlock,
			trace *evals.Trace[*ReviewResult], result **ReviewResult) map[string]any {

			params, errResp := claudetool.NewParams(toolUse)
			if errResp != nil {
				return errResp
			}

			path, errResp := claudetool.Param[string](params, "path")
			if errResp != nil {
				return errResp
			}

			content, err := r.github.GetFileContent(ctx, owner, repo, path, sha)
			if err != nil {
				return claudetool.Error("failed to read file %s: %v", path, err)
			}

			return map[string]any{
				"content": content,
				"path":    path,
			}
		},
	}
}

// geminiReadFileTool creates a Gemini tool for reading file contents.
func (r *Reviewer) geminiReadFileTool(owner, repo, sha string) googleexecutor.ToolMetadata[*ReviewResult] {
	return googleexecutor.ToolMetadata[*ReviewResult]{
		Definition: &genai.FunctionDeclaration{
			Name:        "read_file",
			Description: "Read the full content of a file in the PR for additional context",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"path": {
						Type:        genai.TypeString,
						Description: "File path relative to repository root",
					},
				},
				Required: []string{"path"},
			},
		},
		Handler: func(ctx context.Context, call *genai.FunctionCall,
			trace *evals.Trace[*ReviewResult], result **ReviewResult) *genai.FunctionResponse {

			path, errResp := googletool.Param[string](call, "path")
			if errResp != nil {
				return errResp
			}

			content, err := r.github.GetFileContent(ctx, owner, repo, path, sha)
			if err != nil {
				return googletool.Error(call, "failed to read file %s: %v", path, err)
			}

			return &genai.FunctionResponse{
				ID:   call.ID,
				Name: call.Name,
				Response: map[string]any{
					"content": content,
					"path":    path,
				},
			}
		},
	}
}

// PostReview submits the review to GitHub.
func (r *Reviewer) PostReview(ctx context.Context, owner, repo string, prNumber int, result *ReviewResult, commitSHA string) error {
	if r.github == nil {
		return fmt.Errorf("GitHub client not set")
	}

	// Build the review body with inline suggestions as markdown
	var body strings.Builder
	body.WriteString(result.Summary)

	if len(result.Suggestions) > 0 {
		body.WriteString("\n\n---\n\n## Suggestions\n\n")
		for i, s := range result.Suggestions {
			body.WriteString(fmt.Sprintf("### %d. `%s` (lines %d-%d) - %s\n\n",
				i+1, s.File, s.LineStart, s.LineEnd, strings.ToUpper(s.Severity)))
			body.WriteString(s.Message)
			if s.Suggestion != "" {
				body.WriteString(fmt.Sprintf("\n\n```suggestion\n%s\n```", s.Suggestion))
			}
			body.WriteString("\n\n")
		}
	}

	// Determine review event
	// Note: REQUEST_CHANGES doesn't work on your own PRs, so we use COMMENT for non-approved reviews
	event := "COMMENT"
	if result.Approved {
		event = "APPROVE"
	}

	// Submit review without inline comments (they require exact diff line matching)
	// Instead, include all suggestions in the review body
	review := &gh.PullRequestReviewRequest{
		CommitID: ghclient.Ptr(commitSHA),
		Body:     ghclient.Ptr(body.String()),
		Event:    ghclient.Ptr(event),
	}

	_, err := r.github.CreateReview(ctx, owner, repo, prNumber, review)
	if err != nil {
		return fmt.Errorf("create review: %w", err)
	}

	return nil
}

// formatFiles creates a formatted string of changed files.
func formatFiles(files []*gh.CommitFile) string {
	var sb strings.Builder
	for _, f := range files {
		status := f.GetStatus()
		filename := f.GetFilename()
		additions := f.GetAdditions()
		deletions := f.GetDeletions()

		sb.WriteString(fmt.Sprintf("- %s (%s, +%d/-%d)\n", filename, status, additions, deletions))
	}
	return sb.String()
}

// hasErrors checks if any suggestions have error severity.
func hasErrors(suggestions []CodeSuggestion) bool {
	for _, s := range suggestions {
		if strings.ToLower(s.Severity) == "error" {
			return true
		}
	}
	return false
}

// GetCommitSHA returns the head SHA of a PR.
func (r *Reviewer) GetCommitSHA(ctx context.Context, owner, repo string, prNumber int) (string, error) {
	pr, err := r.github.GetPR(ctx, owner, repo, prNumber)
	if err != nil {
		return "", err
	}
	return pr.GetHead().GetSHA(), nil
}
