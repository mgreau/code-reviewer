/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	ghclient "github.com/example/code-reviewer/pkg/github"
	"github.com/example/code-reviewer/pkg/reviewer"
	"github.com/example/code-reviewer/pkg/skills"
	"github.com/example/code-reviewer/pkg/webhook"
	"github.com/example/code-reviewer/pkg/workdir"
	gh "github.com/google/go-github/v68/github"
)

func runServe() {
	addr := flag.String("addr", ":8080", "Listen address")
	botLogin := flag.String("bot-login", "", "Mention trigger login (e.g. 'openreview' or 'code-reviewer'). Required.")
	provider := flag.String("provider", "claude", "AI provider: claude or gemini")
	model := flag.String("model", "", "Model name (empty = provider default)")
	clone := flag.Bool("clone", true, "Clone PR branch for each review (enables bash/write_file tools)")
	apply := flag.Bool("apply", false, "Commit and push agent-made changes back to the PR branch")
	skillsDir := flag.String("skills", ".agents/skills", "Directory to scan for SKILL.md files when -clone is off")
	useJudge := flag.Bool("judge", false, "Enable the quality judge")
	judgeMinScore := flag.Float64("judge-min-score", 0.5, "Minimum judge score for inclusion")
	flag.Parse()

	if *botLogin == "" {
		log.Fatal("-bot-login is required (e.g. -bot-login=openreview)")
	}
	if *provider != "claude" && *provider != "gemini" {
		log.Fatalf("Invalid provider %q", *provider)
	}

	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if projectID == "" {
		log.Fatal("GOOGLE_CLOUD_PROJECT environment variable is required")
	}
	location := os.Getenv("GOOGLE_CLOUD_LOCATION")
	if location == "" {
		location = "us-east5"
	}
	githubToken := os.Getenv("GITHUB_TOKEN")
	if githubToken == "" {
		log.Fatal("GITHUB_TOKEN environment variable is required")
	}
	webhookSecret := os.Getenv("GITHUB_WEBHOOK_SECRET")

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	mux.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		body, err := webhook.Verify(r, webhookSecret)
		if err != nil {
			if errors.Is(err, webhook.ErrInvalidSignature) {
				http.Error(w, "invalid signature", http.StatusUnauthorized)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		switch webhook.Event(r) {
		case "ping":
			_, _ = w.Write([]byte("pong"))
			return
		case "issue_comment":
			ev, err := webhook.ParseIssueComment(body)
			if err != nil {
				http.Error(w, "parse payload: "+err.Error(), http.StatusBadRequest)
				return
			}
			if ev.Action != "created" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if !ev.IsPRComment() {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			cmd, ok := ev.ParseCommand(*botLogin)
			if !ok {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			// Kick off the command asynchronously so GitHub receives a fast
			// 202 and our HTTP timeout stays under 10s.
			go handleMention(context.Background(), logger, handleArgs{
				ev:            ev,
				cmd:           cmd,
				botLogin:      *botLogin,
				provider:      *provider,
				model:         *model,
				clone:         *clone,
				apply:         *apply,
				skillsDir:     *skillsDir,
				useJudge:      *useJudge,
				judgeMinScore: *judgeMinScore,
				projectID:     projectID,
				location:      location,
				githubToken:   githubToken,
			})
			w.WriteHeader(http.StatusAccepted)
			return
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})

	server := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	logger.Info("starting webhook server", "addr", *addr, "bot", *botLogin, "provider", *provider)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

type handleArgs struct {
	ev            *webhook.IssueCommentEvent
	cmd           webhook.Command
	botLogin      string
	provider      string
	model         string
	clone         bool
	apply         bool
	skillsDir     string
	useJudge      bool
	judgeMinScore float64
	projectID     string
	location      string
	githubToken   string
}

// handleMention dispatches a parsed @bot command from a PR comment. Each
// branch posts a best-effort failure comment if something goes wrong so the
// user isn't left guessing.
func handleMention(ctx context.Context, logger *slog.Logger, a handleArgs) {
	owner := a.ev.Repository.Owner.Login
	repo := a.ev.Repository.Name
	pr := a.ev.Issue.Number
	log := logger.With("owner", owner, "repo", repo, "pr", pr, "cmd", a.cmd.Type)
	log.Info("handling command", "triggered_by", a.ev.Comment.User.Login, "cmd", a.cmd.Type)

	githubClient := ghclient.NewClient(ctx, a.githubToken)

	// Acknowledge the mention with an eyes reaction before we start working.
	if err := githubClient.AddIssueCommentReaction(ctx, owner, repo, a.ev.Comment.ID, "eyes"); err != nil {
		log.Warn("ack reaction failed", "err", err)
	}

	switch a.cmd.Type {
	case webhook.CmdApply:
		handleApply(ctx, log, githubClient, a)
	case webhook.CmdSkip:
		handleSkip(ctx, log, githubClient, a)
	default:
		handleReview(ctx, log, githubClient, a)
	}
}

// handleReview runs a full code review and posts the result. This covers both
// CmdReview (default / explicit `review`) and CmdRereview.
func handleReview(ctx context.Context, log *slog.Logger, githubClient *ghclient.Client, a handleArgs) {
	owner := a.ev.Repository.Owner.Login
	repo := a.ev.Repository.Name
	pr := a.ev.Issue.Number

	rev, err := buildReviewer(ctx, a)
	if err != nil {
		log.Error("create reviewer failed", "err", err)
		postFailure(ctx, githubClient, owner, repo, pr, err)
		return
	}
	rev.SetGitHub(githubClient)

	// Resolve and record the authenticated login so new inline comments can
	// be numbered (and later referenced by `@bot apply N`).
	if login, err := githubClient.GetAuthenticatedLogin(ctx); err == nil {
		rev.SetBotLogin(login)
	} else {
		log.Warn("resolve bot login failed", "err", err)
	}

	wd, err := maybeClone(ctx, log, githubClient, a)
	if err != nil {
		postFailure(ctx, githubClient, owner, repo, pr, err)
		return
	}
	if wd != nil {
		defer func() { _ = wd.Close() }()
		rev.SetWorkdir(wd)
	}

	attachSkills(rev, wd, a.skillsDir)

	if comments, err := githubClient.ListIssueComments(ctx, owner, repo, pr); err == nil {
		rev.SetConversation(formatConversation(comments, a.ev.Comment.ID))
	} else {
		log.Warn("list comments failed", "err", err)
	}

	output, err := rev.Review(ctx, owner, repo, pr)
	if err != nil {
		log.Error("review failed", "err", err)
		postFailure(ctx, githubClient, owner, repo, pr, err)
		return
	}

	if a.useJudge {
		cfg := reviewer.JudgeConfig{Enabled: true, Model: reviewer.DefaultGeminiModel, MinScore: a.judgeMinScore}
		if judged, err := reviewer.JudgeSuggestions(ctx, a.projectID, a.location, cfg, output.Result.Suggestions); err == nil {
			output.Result.Suggestions = reviewer.ExtractSuggestions(judged)
		}
	}

	if a.apply && wd != nil {
		if changed, err := wd.HasUncommittedChanges(ctx); err != nil {
			log.Warn("check uncommitted changes failed", "err", err)
		} else if changed {
			if _, err := wd.CommitAndPush(ctx, "code-reviewer: apply changes", wd.Branch); err != nil {
				log.Warn("commit+push failed", "err", err)
			}
		}
	}

	if err := rev.PostReview(ctx, owner, repo, pr, output); err != nil {
		log.Error("post review failed", "err", err)
		postFailure(ctx, githubClient, owner, repo, pr, err)
		return
	}
	log.Info("review completed", "suggestions", len(output.Result.Suggestions))
}

// handleApply applies one or more suggestions referenced by `@bot apply N` /
// `@bot apply all`. Requires a workdir (i.e. -clone) so we can commit+push.
func handleApply(ctx context.Context, log *slog.Logger, githubClient *ghclient.Client, a handleArgs) {
	owner := a.ev.Repository.Owner.Login
	repo := a.ev.Repository.Name
	pr := a.ev.Issue.Number

	if !a.clone {
		msg := "`apply` requires the reviewer to be started with `-clone` so it can commit and push."
		_ = githubClient.CreateIssueComment(ctx, owner, repo, pr, msg)
		return
	}

	rev, err := buildReviewer(ctx, a)
	if err != nil {
		log.Error("create reviewer failed", "err", err)
		postFailure(ctx, githubClient, owner, repo, pr, err)
		return
	}
	rev.SetGitHub(githubClient)

	login, err := githubClient.GetAuthenticatedLogin(ctx)
	if err != nil {
		log.Error("resolve bot login failed", "err", err)
		postFailure(ctx, githubClient, owner, repo, pr, err)
		return
	}
	rev.SetBotLogin(login)

	wd, err := maybeClone(ctx, log, githubClient, a)
	if err != nil {
		postFailure(ctx, githubClient, owner, repo, pr, err)
		return
	}
	if wd == nil {
		postFailure(ctx, githubClient, owner, repo, pr, fmt.Errorf("apply requires a workdir"))
		return
	}
	defer func() { _ = wd.Close() }()
	rev.SetWorkdir(wd)

	res, err := rev.ApplySuggestions(ctx, owner, repo, pr, a.cmd.Nums, a.cmd.All)
	if err != nil {
		log.Error("apply failed", "err", err)
		postFailure(ctx, githubClient, owner, repo, pr, err)
		return
	}

	body := renderApplyReport(res, a.cmd)
	if err := githubClient.CreateIssueComment(ctx, owner, repo, pr, body); err != nil {
		log.Warn("post apply report failed", "err", err)
	}
	log.Info("apply completed", "applied", res.Applied, "missing", res.Missing, "sha", res.PushedSHA)
}

// handleSkip acknowledges `@bot skip N` without changing state. Useful to tell
// the bot (and any human readers) that a suggestion was considered and
// intentionally dismissed.
func handleSkip(ctx context.Context, log *slog.Logger, githubClient *ghclient.Client, a handleArgs) {
	owner := a.ev.Repository.Owner.Login
	repo := a.ev.Repository.Name
	pr := a.ev.Issue.Number

	var label string
	switch {
	case a.cmd.All:
		label = "all suggestions"
	case len(a.cmd.Nums) == 0:
		label = "no suggestion specified"
	case len(a.cmd.Nums) == 1:
		label = fmt.Sprintf("suggestion #%d", a.cmd.Nums[0])
	default:
		parts := make([]string, len(a.cmd.Nums))
		for i, n := range a.cmd.Nums {
			parts[i] = fmt.Sprintf("#%d", n)
		}
		label = "suggestions " + strings.Join(parts, ", ")
	}
	body := fmt.Sprintf("Acknowledged — skipping %s.", label)
	if err := githubClient.CreateIssueComment(ctx, owner, repo, pr, body); err != nil {
		log.Warn("post skip ack failed", "err", err)
	}
}

// buildReviewer instantiates a Reviewer for the configured provider.
func buildReviewer(ctx context.Context, a handleArgs) (*reviewer.Reviewer, error) {
	switch a.provider {
	case "claude":
		return reviewer.NewWithClaude(ctx, a.projectID, a.location, a.model)
	case "gemini":
		return reviewer.NewWithGemini(ctx, a.projectID, a.location, a.model)
	default:
		return nil, fmt.Errorf("unknown provider %q", a.provider)
	}
}

// maybeClone shallow-clones the PR branch when -clone is set. Returns a nil
// workdir with nil error when -clone is off.
func maybeClone(ctx context.Context, log *slog.Logger, githubClient *ghclient.Client, a handleArgs) (*workdir.Workdir, error) {
	if !a.clone {
		return nil, nil
	}
	owner := a.ev.Repository.Owner.Login
	repo := a.ev.Repository.Name
	pr := a.ev.Issue.Number

	prMeta, err := githubClient.GetPR(ctx, owner, repo, pr)
	if err != nil {
		return nil, fmt.Errorf("fetch PR: %w", err)
	}
	branch := prMeta.GetHead().GetRef()
	wd, err := workdir.Clone(ctx, fmt.Sprintf("%s/%s", owner, repo), branch, a.githubToken)
	if err != nil {
		return nil, fmt.Errorf("clone: %w", err)
	}
	if err := wd.ConfigureGit(ctx, a.githubToken, "", ""); err != nil {
		log.Warn("configure git failed", "err", err)
	}
	return wd, nil
}

// attachSkills loads SKILL.md catalogues from the workdir (if present) and the
// configured fallback directory, and attaches them to the reviewer.
func attachSkills(rev *reviewer.Reviewer, wd *workdir.Workdir, fallback string) {
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

// renderApplyReport turns an ApplyResult into a human-readable PR comment.
func renderApplyReport(res *reviewer.ApplyResult, cmd webhook.Command) string {
	var b strings.Builder
	if len(res.Applied) == 0 {
		b.WriteString("No suggestions applied.\n")
	} else {
		parts := make([]string, len(res.Applied))
		for i, n := range res.Applied {
			parts[i] = fmt.Sprintf("#%d", n)
		}
		fmt.Fprintf(&b, "Applied %d suggestion(s): %s.\n", len(res.Applied), strings.Join(parts, ", "))
	}
	if res.PushedSHA != "" {
		fmt.Fprintf(&b, "\nPushed `%s` to the PR branch.\n", res.PushedSHA)
	} else if len(res.Applied) > 0 {
		b.WriteString("\nNo file changes were produced (suggestions matched current content).\n")
	}
	if len(res.Missing) > 0 {
		parts := make([]string, len(res.Missing))
		for i, n := range res.Missing {
			parts[i] = fmt.Sprintf("#%d", n)
		}
		fmt.Fprintf(&b, "\n**Skipped** (missing or malformed): %s.\n", strings.Join(parts, ", "))
	}
	if !cmd.All && len(cmd.Nums) == 0 {
		b.WriteString("\n> `apply` with no index defaults to applying nothing. Use `@bot apply all` or `@bot apply N`.\n")
	}
	return b.String()
}

func postFailure(ctx context.Context, client *ghclient.Client, owner, repo string, pr int, err error) {
	body := fmt.Sprintf(`## Review failed

%s

---
*Powered by [code-reviewer](https://github.com/chainguard-dev/code-reviewer)*`, err)
	_ = client.CreateIssueComment(ctx, owner, repo, pr, body)
}

// formatConversation renders the PR comment thread as a plain-text log, with
// the triggering comment pinned last so the agent sees it as the most recent
// message. When triggerID matches a comment it is promoted to the tail.
func formatConversation(comments []*gh.IssueComment, triggerID int64) string {
	var ordered []*gh.IssueComment
	var trigger *gh.IssueComment
	for _, c := range comments {
		if c.GetID() == triggerID {
			trigger = c
			continue
		}
		ordered = append(ordered, c)
	}
	if trigger != nil {
		ordered = append(ordered, trigger)
	}
	var b strings.Builder
	for _, c := range ordered {
		author := c.GetUser().GetLogin()
		when := c.GetCreatedAt().Format(time.RFC3339)
		fmt.Fprintf(&b, "[%s %s]\n%s\n\n", when, author, c.GetBody())
	}
	return b.String()
}
