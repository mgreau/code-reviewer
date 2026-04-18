/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package reviewer

import (
	"context"
	"fmt"
	"log/slog"

	ghclient "github.com/example/code-reviewer/pkg/github"
	"github.com/example/code-reviewer/pkg/skills"
	"github.com/example/code-reviewer/pkg/workdir"
)

// RunnerConfig holds the knobs RunReview needs to produce and post a review.
// Everything the reviewer would otherwise read from flags lives here so the
// reconciler and the webhook server can share one implementation.
type RunnerConfig struct {
	Provider      string
	Model         string
	ProjectID     string
	Location      string
	GitHubToken   string
	Clone         bool
	Apply         bool
	SkillsDir     string
	UseJudge      bool
	JudgeMinScore float64
}

// RunReview runs a fresh review against the given PR and posts the result.
// When Clone is set, it shallow-clones the PR branch so the agent gets bash
// and write_file. When Apply is set and the agent wrote files, those changes
// are committed and pushed before the review is posted.
//
// This is the reconciler's one job. The webhook server uses it too, via
// handleReview in cmd/reviewer/serve.go.
func RunReview(ctx context.Context, log *slog.Logger, cfg RunnerConfig, owner, repo string, pr int) error {
	rev, err := build(ctx, cfg)
	if err != nil {
		return fmt.Errorf("build reviewer: %w", err)
	}

	client := ghclient.NewClient(ctx, cfg.GitHubToken)
	rev.SetGitHub(client)
	if login, err := client.GetAuthenticatedLogin(ctx); err == nil {
		rev.SetBotLogin(login)
	} else {
		log.Warn("resolve bot login failed", "err", err)
	}

	var wd *workdir.Workdir
	if cfg.Clone {
		prMeta, err := client.GetPR(ctx, owner, repo, pr)
		if err != nil {
			return fmt.Errorf("fetch PR: %w", err)
		}
		branch := prMeta.GetHead().GetRef()
		wd, err = workdir.Clone(ctx, fmt.Sprintf("%s/%s", owner, repo), branch, cfg.GitHubToken)
		if err != nil {
			return fmt.Errorf("clone: %w", err)
		}
		defer func() { _ = wd.Close() }()
		if err := wd.ConfigureGit(ctx, cfg.GitHubToken, "", ""); err != nil {
			log.Warn("configure git failed", "err", err)
		}
		rev.SetWorkdir(wd)
	}

	AttachSkills(rev, wd, cfg.SkillsDir)

	output, err := rev.Review(ctx, owner, repo, pr)
	if err != nil {
		return fmt.Errorf("review: %w", err)
	}

	if cfg.UseJudge {
		jc := JudgeConfig{Enabled: true, Model: DefaultGeminiModel, MinScore: cfg.JudgeMinScore}
		if judged, err := JudgeSuggestions(ctx, cfg.ProjectID, cfg.Location, jc, output.Result.Suggestions); err == nil {
			output.Result.Suggestions = ExtractSuggestions(judged)
		} else {
			log.Warn("judge failed, keeping unjudged suggestions", "err", err)
		}
	}

	if cfg.Apply && wd != nil {
		if changed, err := wd.HasUncommittedChanges(ctx); err == nil && changed {
			if _, err := wd.CommitAndPush(ctx, "code-reviewer: apply changes", wd.Branch); err != nil {
				log.Warn("commit+push failed", "err", err)
			}
		}
	}

	if err := rev.PostReview(ctx, owner, repo, pr, output); err != nil {
		return fmt.Errorf("post review: %w", err)
	}

	log.Info("review completed", "suggestions", len(output.Result.Suggestions))
	return nil
}

// AttachSkills discovers SKILL.md catalogues from the workdir (if present)
// and the fallback directory, and attaches them to the reviewer. Exported
// so serve.go and the reconciler use the same discovery order.
func AttachSkills(rev *Reviewer, wd *workdir.Workdir, fallback string) {
	var dirs []string
	if wd != nil {
		dirs = append(dirs, fmt.Sprintf("%s/.agents/skills", wd.Root))
	}
	if fallback != "" {
		dirs = append(dirs, fallback)
	}
	if discovered, err := skills.Discover(dirs...); err == nil && len(discovered) > 0 {
		rev.SetSkills(discovered)
	}
}

func build(ctx context.Context, cfg RunnerConfig) (*Reviewer, error) {
	switch cfg.Provider {
	case "claude":
		return NewWithClaude(ctx, cfg.ProjectID, cfg.Location, cfg.Model)
	case "gemini":
		return NewWithGemini(ctx, cfg.ProjectID, cfg.Location, cfg.Model)
	default:
		return nil, fmt.Errorf("unknown provider %q", cfg.Provider)
	}
}
