/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package reviewer

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"chainguard.dev/driftlessaf/agents/agenttrace"
	"chainguard.dev/driftlessaf/agents/executor/claudeexecutor"
	"chainguard.dev/driftlessaf/agents/executor/googleexecutor"
	"chainguard.dev/driftlessaf/agents/submitresult"
	"chainguard.dev/driftlessaf/agents/toolcall/claudetool"
	"chainguard.dev/driftlessaf/agents/toolcall/googletool"
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
	"github.com/anthropics/anthropic-sdk-go/vertex"
	ghclient "github.com/example/code-reviewer/pkg/github"
	"github.com/example/code-reviewer/pkg/skills"
	"github.com/example/code-reviewer/pkg/workdir"
	gh "github.com/google/go-github/v68/github"
	"google.golang.org/genai"
)

// Default models for each provider. Sonnet 4.6 matches openreview's default.
const (
	DefaultClaudeModel = "claude-sonnet-4-6@20251022"
	DefaultGeminiModel = "gemini-2.5-flash"
)

// Reviewer orchestrates PR code reviews using AI.
type Reviewer struct {
	github     *ghclient.Client
	claudeExec claudeexecutor.Interface[*ReviewRequest, *ReviewResult]
	googleExec googleexecutor.Interface[*ReviewRequest, *ReviewResult]
	provider   string

	// workdir, when non-nil, grants the agent filesystem access via bash,
	// read_file (local), and write_file tools. Openreview parity.
	workdir *workdir.Workdir

	// skills is the progressive skill catalogue. Names and descriptions are
	// surfaced in the system prompt; full bodies load via the load_skill tool.
	skills []skills.Skill

	// conversation is the prior PR-comment thread included in the prompt.
	// Populated when a review is triggered by an @mention so the agent can
	// see earlier messages.
	conversation string

	// botLogin is the authenticated GitHub user. When set, PostReview numbers
	// new inline comments globally (continuing from existing bot comments on
	// the PR) so users can reference them as `@bot apply N`. ApplySuggestions
	// also uses this to filter bot-authored comments.
	botLogin string
}

// NewWithClaude creates a new Reviewer using Claude via Vertex AI.
// model may be empty, in which case DefaultClaudeModel is used.
func NewWithClaude(ctx context.Context, projectID, location, model string) (*Reviewer, error) {
	if model == "" {
		model = DefaultClaudeModel
	}
	client := anthropic.NewClient(
		vertex.WithGoogleAuth(ctx, location, projectID),
	)

	exec, err := claudeexecutor.New[*ReviewRequest, *ReviewResult](
		client,
		ReviewPrompt,
		claudeexecutor.WithModel[*ReviewRequest, *ReviewResult](model),
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
// model may be empty, in which case DefaultGeminiModel is used.
func NewWithGemini(ctx context.Context, projectID, location, model string) (*Reviewer, error) {
	if model == "" {
		model = DefaultGeminiModel
	}
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
		googleexecutor.WithModel[*ReviewRequest, *ReviewResult](model),
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

// SetWorkdir attaches a working directory so bash/write_file/local read_file
// are available to the agent. When nil, those tools return errors.
func (r *Reviewer) SetWorkdir(w *workdir.Workdir) {
	r.workdir = w
}

// SetSkills registers the skill catalogue surfaced via the load_skill tool.
func (r *Reviewer) SetSkills(s []skills.Skill) {
	r.skills = s
}

// Workdir returns the configured working directory, or nil.
func (r *Reviewer) Workdir() *workdir.Workdir { return r.workdir }

// SetConversation attaches a rendered conversation history. Empty string
// omits the section from the prompt.
func (r *Reviewer) SetConversation(s string) { r.conversation = s }

// SetBotLogin records the authenticated GitHub login. Enables stable numbering
// of inline review comments so users can reference them via `@bot apply N`.
func (r *Reviewer) SetBotLogin(s string) { r.botLogin = s }

// BotLogin returns the configured bot login, or "".
func (r *Reviewer) BotLogin() string { return r.botLogin }

// Review performs a code review on the specified PR.
// Returns a ReviewOutput containing the result, commit SHA, and cached diff for posting.
func (r *Reviewer) Review(ctx context.Context, owner, repo string, prNumber int) (*ReviewOutput, error) {
	log := slog.With("owner", owner, "repo", repo, "pr", prNumber, "provider", r.provider)

	if r.github == nil {
		return nil, fmt.Errorf("GitHub client not set")
	}

	log.Info("fetching PR metadata")
	pr, err := r.github.GetPR(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("fetch PR %s/%s#%d metadata: %w", owner, repo, prNumber, err)
	}

	log.Info("fetching PR diff")
	diff, err := r.github.GetPRDiff(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("fetch PR %s/%s#%d diff: %w", owner, repo, prNumber, err)
	}

	log.Info("fetching changed files")
	files, err := r.github.GetPRFiles(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("fetch PR %s/%s#%d files: %w", owner, repo, prNumber, err)
	}

	log.Info("starting AI review", "files_count", len(files), "diff_size", len(diff))

	// Build the request
	request := &ReviewRequest{
		Repo:         fmt.Sprintf("%s/%s", owner, repo),
		Title:        pr.GetTitle(),
		Description:  pr.GetBody(),
		Files:        formatFiles(files),
		Diff:         diff,
		Skills:       skills.BuildPrompt(r.skills),
		Conversation: r.conversation,
	}

	sha := pr.GetHead().GetSHA()

	// Execute with the appropriate provider
	var result *ReviewResult
	switch r.provider {
	case "claude":
		result, err = r.reviewWithClaude(ctx, request, owner, repo, sha, prNumber)
	case "gemini":
		result, err = r.reviewWithGemini(ctx, request, owner, repo, sha, prNumber)
	default:
		return nil, fmt.Errorf("unknown provider: %s", r.provider)
	}

	if err != nil {
		return nil, err
	}

	log.Info("review completed", "suggestions", len(result.Suggestions), "approved", result.Approved)

	return &ReviewOutput{
		Result:    result,
		CommitSHA: sha,
		Diff:      diff,
	}, nil
}

func (r *Reviewer) reviewWithClaude(ctx context.Context, request *ReviewRequest, owner, repo, sha string, prNumber int) (*ReviewResult, error) {
	tools := map[string]claudetool.Metadata[*ReviewResult]{
		"read_file":  r.claudeReadFileTool(owner, repo, sha),
		"load_skill": r.claudeLoadSkillTool(),
	}
	if r.workdir != nil {
		tools["bash"] = r.claudeBashTool()
		tools["write_file"] = r.claudeWriteFileTool()
	}
	if r.github != nil {
		tools["reply"] = r.claudeReplyTool(owner, repo, prNumber)
	}
	return r.claudeExec.Execute(ctx, request, tools)
}

func (r *Reviewer) reviewWithGemini(ctx context.Context, request *ReviewRequest, owner, repo, sha string, prNumber int) (*ReviewResult, error) {
	tools := map[string]googletool.Metadata[*ReviewResult]{
		"read_file":  r.geminiReadFileTool(owner, repo, sha),
		"load_skill": r.geminiLoadSkillTool(),
	}
	if r.workdir != nil {
		tools["bash"] = r.geminiBashTool()
		tools["write_file"] = r.geminiWriteFileTool()
	}
	if r.github != nil {
		tools["reply"] = r.geminiReplyTool(owner, repo, prNumber)
	}
	return r.googleExec.Execute(ctx, request, tools)
}

// claudeReadFileTool creates a Claude tool for reading file contents.
func (r *Reviewer) claudeReadFileTool(owner, repo, sha string) claudetool.Metadata[*ReviewResult] {
	return claudetool.Metadata[*ReviewResult]{
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
			trace *agenttrace.Trace[*ReviewResult], result **ReviewResult) map[string]any {

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
func (r *Reviewer) geminiReadFileTool(owner, repo, sha string) googletool.Metadata[*ReviewResult] {
	return googletool.Metadata[*ReviewResult]{
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
			trace *agenttrace.Trace[*ReviewResult], result **ReviewResult) *genai.FunctionResponse {

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

// PostReview submits the review to GitHub with inline comments.
// Uses the cached diff from ReviewOutput to avoid re-fetching.
func (r *Reviewer) PostReview(ctx context.Context, owner, repo string, prNumber int, output *ReviewOutput) error {
	log := slog.With("owner", owner, "repo", repo, "pr", prNumber)

	if r.github == nil {
		return fmt.Errorf("GitHub client not set")
	}

	// Parse cached diff to get valid line ranges per file
	diffInfo := newDiffLines(output.Diff)

	result := output.Result

	// Determine the numbering offset so new inline comments continue from
	// where prior bot comments left off. Allows users to reference them via
	// `@bot apply N`.
	numberOffset := 0
	if r.botLogin != "" {
		if existing, err := r.github.ListPullRequestReviewComments(ctx, owner, repo, prNumber); err == nil {
			for _, c := range existing {
				if strings.EqualFold(c.GetUser().GetLogin(), r.botLogin) {
					numberOffset++
				}
			}
		} else {
			log.Warn("count prior bot review comments failed", "err", err)
		}
	}

	// Build inline comments for suggestions with valid line numbers
	var comments []*gh.DraftReviewComment
	var unresolvedSuggestions []CodeSuggestion
	nextIdx := numberOffset + 1

	for _, s := range result.Suggestions {
		// Check if the line is in the diff
		if diffInfo.contains(s.File, s.LineEnd) {
			idx := 0
			if r.botLogin != "" {
				idx = nextIdx
				nextIdx++
			}
			comment := buildReviewComment(s, diffInfo, idx)
			comments = append(comments, comment)
		} else {
			unresolvedSuggestions = append(unresolvedSuggestions, s)
		}
	}

	// Build the review body
	var body strings.Builder
	body.WriteString(result.Summary)

	// Add unresolved suggestions to the body (lines not in diff)
	if len(unresolvedSuggestions) > 0 {
		body.WriteString("\n\n---\n\n## Additional Suggestions (outside diff context)\n\n")
		for i, s := range unresolvedSuggestions {
			body.WriteString(fmt.Sprintf("### %d. `%s` (lines %d-%d) - %s\n\n",
				i+1, s.File, s.LineStart, s.LineEnd, s.NormalizedSeverity()))
			body.WriteString(s.Message)
			if s.Suggestion != "" {
				body.WriteString(fmt.Sprintf("\n\n```suggestion\n%s\n```", s.Suggestion))
			}
			body.WriteString("\n\n")
		}
	}

	// Determine review event
	event := "COMMENT"
	if result.Approved {
		event = "APPROVE"
	}

	log.Info("submitting review",
		"inline_comments", len(comments),
		"unresolved_suggestions", len(unresolvedSuggestions),
		"event", event)

	// Submit review with inline comments
	review := &gh.PullRequestReviewRequest{
		CommitID: ghclient.Ptr(output.CommitSHA),
		Body:     ghclient.Ptr(body.String()),
		Event:    ghclient.Ptr(event),
		Comments: comments,
	}

	_, err := r.github.CreateReview(ctx, owner, repo, prNumber, review)
	if err != nil {
		return fmt.Errorf("post review to %s/%s#%d: %w", owner, repo, prNumber, err)
	}

	log.Info("review posted successfully")
	return nil
}

// extractCodeFromSuggestion extracts raw code from a suggestion that may contain markdown.
// If the suggestion contains markdown code fences (```), it extracts only the code.
// Otherwise, returns the suggestion as-is.
func extractCodeFromSuggestion(suggestion string) string {
	// Look for code fences
	if !strings.Contains(suggestion, "```") {
		return suggestion
	}

	// Find all code blocks and extract the last one (most likely the actual suggestion)
	lines := strings.Split(suggestion, "\n")
	var inCodeBlock bool
	var codeLines []string
	var currentBlock []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inCodeBlock {
				// End of code block - save it
				if len(currentBlock) > 0 {
					codeLines = currentBlock
				}
				currentBlock = nil
				inCodeBlock = false
			} else {
				// Start of code block
				inCodeBlock = true
				currentBlock = nil
			}
			continue
		}

		if inCodeBlock {
			currentBlock = append(currentBlock, line)
		}
	}

	// If we found code blocks, use the extracted code
	if len(codeLines) > 0 {
		return strings.Join(codeLines, "\n")
	}

	// No valid code blocks found, return original
	return suggestion
}

// buildReviewComment creates a GitHub review comment from a suggestion.
// index is the global 1-based number surfaced to users as "[#N]" so they can
// reference the suggestion via `@bot apply N`. Pass 0 to omit the marker.
func buildReviewComment(s CodeSuggestion, diffInfo *diffLines, index int) *gh.DraftReviewComment {
	prefix := ""
	if index > 0 {
		prefix = fmt.Sprintf("[#%d] ", index)
	}
	body := fmt.Sprintf("%s**%s**: %s", prefix, s.NormalizedSeverity(), s.Message)
	if s.Suggestion != "" {
		// Extract raw code from suggestion (in case AI returned markdown)
		code := extractCodeFromSuggestion(s.Suggestion)
		body += fmt.Sprintf("\n\n```suggestion\n%s\n```", code)
	}

	comment := &gh.DraftReviewComment{
		Path: ghclient.Ptr(s.File),
		Body: ghclient.Ptr(body),
		Line: ghclient.Ptr(s.LineEnd),
		Side: ghclient.Ptr("RIGHT"),
	}

	// Multi-line comment if start line is also in diff
	if s.LineStart != s.LineEnd && s.LineStart > 0 && diffInfo.contains(s.File, s.LineStart) {
		comment.StartLine = ghclient.Ptr(s.LineStart)
		comment.StartSide = ghclient.Ptr("RIGHT")
	}

	return comment
}

// lineRange represents a contiguous range of valid lines in a diff hunk.
type lineRange struct {
	start int
	end   int
}

// diffLines stores the valid line ranges for files in a diff.
type diffLines struct {
	files map[string][]lineRange
}

// newDiffLines parses a unified diff and extracts valid line numbers.
func newDiffLines(diff string) *diffLines {
	dl := &diffLines{files: make(map[string][]lineRange)}
	lines := strings.Split(diff, "\n")

	var (
		currentFile  string
		currentLine  int
		rangeStart   int
		inRange      bool
	)

	flushRange := func() {
		if inRange && currentFile != "" && rangeStart > 0 {
			dl.files[currentFile] = append(dl.files[currentFile], lineRange{
				start: rangeStart,
				end:   currentLine - 1,
			})
			inRange = false
		}
	}

	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "+++ b/"):
			flushRange()
			currentFile = strings.TrimPrefix(line, "+++ b/")
			currentLine = 0

		case strings.HasPrefix(line, "@@"):
			flushRange()
			// Parse hunk header: @@ -old,count +new,count @@
			if start := parseHunkStart(line); start > 0 {
				currentLine = start
			}

		case currentFile != "" && currentLine > 0:
			switch {
			case strings.HasPrefix(line, "+"), strings.HasPrefix(line, " "):
				// Line exists in new version - track it
				if !inRange {
					rangeStart = currentLine
					inRange = true
				}
				currentLine++

			case strings.HasPrefix(line, "-"):
				// Removed line - flush current range, don't increment
				flushRange()

			case strings.HasPrefix(line, "\\"):
				// "\ No newline at end of file" - ignore

			default:
				// Context line continuation
				if !inRange {
					rangeStart = currentLine
					inRange = true
				}
				currentLine++
			}
		}
	}

	flushRange()
	return dl
}

// parseHunkStart extracts the starting line number from a hunk header.
// Format: @@ -old,count +new,count @@ optional context
func parseHunkStart(header string) int {
	// Find the +N part
	plusIdx := strings.Index(header, "+")
	if plusIdx == -1 {
		return 0
	}

	// Extract number after +
	rest := header[plusIdx+1:]
	var start int
	for i, c := range rest {
		if c >= '0' && c <= '9' {
			start = start*10 + int(c-'0')
		} else if i > 0 {
			break
		}
	}
	return start
}

// contains checks if a line number is valid in the diff for the given file.
func (dl *diffLines) contains(file string, line int) bool {
	ranges, ok := dl.files[file]
	if !ok {
		return false
	}

	for _, r := range ranges {
		if line >= r.start && line <= r.end {
			return true
		}
	}
	return false
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

