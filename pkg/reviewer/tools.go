/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package reviewer

import (
	"context"
	"fmt"

	"chainguard.dev/driftlessaf/agents/agenttrace"
	"chainguard.dev/driftlessaf/agents/toolcall/claudetool"
	"chainguard.dev/driftlessaf/agents/toolcall/googletool"
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
	"github.com/example/code-reviewer/pkg/skills"
	"google.golang.org/genai"
)

// toolTruncateLimit caps tool-response payloads so giant stdout/read_file
// results do not blow through the context window. Mirrors openreview's 10k
// cap in workflow/steps/run-agent.ts.
const toolTruncateLimit = 10_000

// claudeBashTool executes a shell command inside the working directory.
func (r *Reviewer) claudeBashTool() claudetool.Metadata[*ReviewResult] {
	return claudetool.Metadata[*ReviewResult]{
		Definition: anthropic.ToolParam{
			Name:        "bash",
			Description: anthropic.String("Execute a bash command inside the working directory. Use to run linters, formatters, tests, or explore the repo."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Type: constant.Object("object"),
				Properties: map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "The bash command to execute",
					},
				},
				Required: []string{"command"},
			},
		},
		Handler: func(ctx context.Context, use anthropic.ToolUseBlock,
			_ *agenttrace.Trace[*ReviewResult], _ **ReviewResult) map[string]any {
			params, errResp := claudetool.NewParams(use)
			if errResp != nil {
				return errResp
			}
			command, errResp := claudetool.Param[string](params, "command")
			if errResp != nil {
				return errResp
			}
			if r.workdir == nil {
				return claudetool.Error("bash tool unavailable: no working directory configured (run with -workdir or -clone)")
			}
			stdout, stderr, code, err := r.workdir.Bash(ctx, command)
			if err != nil {
				return claudetool.Error("bash execution failed: %v", err)
			}
			return map[string]any{
				"exit_code": code,
				"stdout":    truncate(stdout),
				"stderr":    truncate(stderr),
			}
		},
	}
}

// claudeWriteFileTool writes content to a file in the working directory.
func (r *Reviewer) claudeWriteFileTool() claudetool.Metadata[*ReviewResult] {
	return claudetool.Metadata[*ReviewResult]{
		Definition: anthropic.ToolParam{
			Name:        "write_file",
			Description: anthropic.String("Write content to a file in the working directory. Parent directories are created automatically."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Type: constant.Object("object"),
				Properties: map[string]any{
					"path":    map[string]any{"type": "string", "description": "File path relative to the working directory"},
					"content": map[string]any{"type": "string", "description": "Content to write"},
				},
				Required: []string{"path", "content"},
			},
		},
		Handler: func(ctx context.Context, use anthropic.ToolUseBlock,
			_ *agenttrace.Trace[*ReviewResult], _ **ReviewResult) map[string]any {
			_ = ctx
			params, errResp := claudetool.NewParams(use)
			if errResp != nil {
				return errResp
			}
			path, errResp := claudetool.Param[string](params, "path")
			if errResp != nil {
				return errResp
			}
			content, errResp := claudetool.Param[string](params, "content")
			if errResp != nil {
				return errResp
			}
			if r.workdir == nil {
				return claudetool.Error("write_file tool unavailable: no working directory configured")
			}
			if err := r.workdir.WriteFile(path, content); err != nil {
				return claudetool.Error("write_file failed: %v", err)
			}
			return map[string]any{"success": true, "path": path}
		},
	}
}

// claudeLoadSkillTool returns the body of a named skill to the agent.
func (r *Reviewer) claudeLoadSkillTool() claudetool.Metadata[*ReviewResult] {
	return claudetool.Metadata[*ReviewResult]{
		Definition: anthropic.ToolParam{
			Name:        "load_skill",
			Description: anthropic.String("Load specialized review instructions for a specific domain. Use when the request matches an available skill."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Type: constant.Object("object"),
				Properties: map[string]any{
					"name": map[string]any{"type": "string", "description": "The skill name to load"},
				},
				Required: []string{"name"},
			},
		},
		Handler: func(_ context.Context, use anthropic.ToolUseBlock,
			_ *agenttrace.Trace[*ReviewResult], _ **ReviewResult) map[string]any {
			params, errResp := claudetool.NewParams(use)
			if errResp != nil {
				return errResp
			}
			name, errResp := claudetool.Param[string](params, "name")
			if errResp != nil {
				return errResp
			}
			s := skills.Find(r.skills, name)
			if s == nil {
				return claudetool.Error("skill %q not found. Available: %s", name, availableSkillNames(r.skills))
			}
			return map[string]any{"name": s.Name, "content": s.Content}
		},
	}
}

// claudeReplyTool posts a top-level comment on the PR.
func (r *Reviewer) claudeReplyTool(owner, repo string, prNumber int) claudetool.Metadata[*ReviewResult] {
	return claudetool.Metadata[*ReviewResult]{
		Definition: anthropic.ToolParam{
			Name:        "reply",
			Description: anthropic.String("Post a markdown comment on the pull request. Use to share progress, findings, or clarifying questions."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Type: constant.Object("object"),
				Properties: map[string]any{
					"body": map[string]any{"type": "string", "description": "Markdown-formatted comment body"},
				},
				Required: []string{"body"},
			},
		},
		Handler: func(ctx context.Context, use anthropic.ToolUseBlock,
			_ *agenttrace.Trace[*ReviewResult], _ **ReviewResult) map[string]any {
			params, errResp := claudetool.NewParams(use)
			if errResp != nil {
				return errResp
			}
			body, errResp := claudetool.Param[string](params, "body")
			if errResp != nil {
				return errResp
			}
			if r.github == nil {
				return claudetool.Error("reply tool unavailable: no GitHub client configured")
			}
			if err := r.github.CreateIssueComment(ctx, owner, repo, prNumber, body); err != nil {
				return claudetool.Error("reply failed: %v", err)
			}
			return map[string]any{"success": true}
		},
	}
}

// geminiBashTool mirrors claudeBashTool for the Gemini executor.
func (r *Reviewer) geminiBashTool() googletool.Metadata[*ReviewResult] {
	return googletool.Metadata[*ReviewResult]{
		Definition: &genai.FunctionDeclaration{
			Name:        "bash",
			Description: "Execute a bash command inside the working directory. Use to run linters, formatters, tests, or explore the repo.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"command": {Type: genai.TypeString, Description: "The bash command to execute"},
				},
				Required: []string{"command"},
			},
		},
		Handler: func(ctx context.Context, call *genai.FunctionCall,
			_ *agenttrace.Trace[*ReviewResult], _ **ReviewResult) *genai.FunctionResponse {
			command, errResp := googletool.Param[string](call, "command")
			if errResp != nil {
				return errResp
			}
			if r.workdir == nil {
				return googletool.Error(call, "bash tool unavailable: no working directory configured")
			}
			stdout, stderr, code, err := r.workdir.Bash(ctx, command)
			if err != nil {
				return googletool.Error(call, "bash execution failed: %v", err)
			}
			return &genai.FunctionResponse{
				ID:   call.ID,
				Name: call.Name,
				Response: map[string]any{
					"exit_code": code,
					"stdout":    truncate(stdout),
					"stderr":    truncate(stderr),
				},
			}
		},
	}
}

func (r *Reviewer) geminiWriteFileTool() googletool.Metadata[*ReviewResult] {
	return googletool.Metadata[*ReviewResult]{
		Definition: &genai.FunctionDeclaration{
			Name:        "write_file",
			Description: "Write content to a file in the working directory. Parent directories are created automatically.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"path":    {Type: genai.TypeString, Description: "File path relative to the working directory"},
					"content": {Type: genai.TypeString, Description: "Content to write"},
				},
				Required: []string{"path", "content"},
			},
		},
		Handler: func(_ context.Context, call *genai.FunctionCall,
			_ *agenttrace.Trace[*ReviewResult], _ **ReviewResult) *genai.FunctionResponse {
			path, errResp := googletool.Param[string](call, "path")
			if errResp != nil {
				return errResp
			}
			content, errResp := googletool.Param[string](call, "content")
			if errResp != nil {
				return errResp
			}
			if r.workdir == nil {
				return googletool.Error(call, "write_file tool unavailable: no working directory configured")
			}
			if err := r.workdir.WriteFile(path, content); err != nil {
				return googletool.Error(call, "write_file failed: %v", err)
			}
			return &genai.FunctionResponse{
				ID:       call.ID,
				Name:     call.Name,
				Response: map[string]any{"success": true, "path": path},
			}
		},
	}
}

func (r *Reviewer) geminiLoadSkillTool() googletool.Metadata[*ReviewResult] {
	return googletool.Metadata[*ReviewResult]{
		Definition: &genai.FunctionDeclaration{
			Name:        "load_skill",
			Description: "Load specialized review instructions for a specific domain. Use when the request matches an available skill.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"name": {Type: genai.TypeString, Description: "The skill name to load"},
				},
				Required: []string{"name"},
			},
		},
		Handler: func(_ context.Context, call *genai.FunctionCall,
			_ *agenttrace.Trace[*ReviewResult], _ **ReviewResult) *genai.FunctionResponse {
			name, errResp := googletool.Param[string](call, "name")
			if errResp != nil {
				return errResp
			}
			s := skills.Find(r.skills, name)
			if s == nil {
				return googletool.Error(call, "skill %q not found. Available: %s", name, availableSkillNames(r.skills))
			}
			return &genai.FunctionResponse{
				ID:       call.ID,
				Name:     call.Name,
				Response: map[string]any{"name": s.Name, "content": s.Content},
			}
		},
	}
}

func (r *Reviewer) geminiReplyTool(owner, repo string, prNumber int) googletool.Metadata[*ReviewResult] {
	return googletool.Metadata[*ReviewResult]{
		Definition: &genai.FunctionDeclaration{
			Name:        "reply",
			Description: "Post a markdown comment on the pull request. Use to share progress, findings, or clarifying questions.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"body": {Type: genai.TypeString, Description: "Markdown-formatted comment body"},
				},
				Required: []string{"body"},
			},
		},
		Handler: func(ctx context.Context, call *genai.FunctionCall,
			_ *agenttrace.Trace[*ReviewResult], _ **ReviewResult) *genai.FunctionResponse {
			body, errResp := googletool.Param[string](call, "body")
			if errResp != nil {
				return errResp
			}
			if r.github == nil {
				return googletool.Error(call, "reply tool unavailable: no GitHub client configured")
			}
			if err := r.github.CreateIssueComment(ctx, owner, repo, prNumber, body); err != nil {
				return googletool.Error(call, "reply failed: %v", err)
			}
			return &genai.FunctionResponse{
				ID:       call.ID,
				Name:     call.Name,
				Response: map[string]any{"success": true},
			}
		},
	}
}

func truncate(s string) string {
	if len(s) <= toolTruncateLimit {
		return s
	}
	return fmt.Sprintf("%s\n\n... (truncated %d chars)", s[:toolTruncateLimit], len(s)-toolTruncateLimit)
}

func availableSkillNames(list []skills.Skill) string {
	if len(list) == 0 {
		return "(none)"
	}
	names := make([]string, len(list))
	for i, s := range list {
		names[i] = s.Name
	}
	return fmt.Sprintf("%v", names)
}

