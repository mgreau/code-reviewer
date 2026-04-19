/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

// Command reconciler is the Cloud Run entrypoint for the workqueue-driven
// deployment. It implements the driftlessaf workqueue gRPC service: keys are
// GitHub PR URLs, and Process(key) runs a fresh review against that PR.
//
// See deploy/terraform/ for the Terraform that wires the workqueue, the
// github-events webhook bridge, and the CloudEvents broker up to this service.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"chainguard.dev/driftlessaf/workqueue"
	"github.com/sethvargo/go-envconfig"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthgrpc "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/example/code-reviewer/pkg/reviewer"
)

type config struct {
	Port          int     `env:"PORT,default=8080"`
	Provider      string  `env:"PROVIDER,default=claude"`
	Model         string  `env:"MODEL,default="`
	Clone         bool    `env:"CLONE,default=true"`
	Apply         bool    `env:"APPLY,default=false"`
	SkillsDir     string  `env:"SKILLS_DIR,default=.agents/skills"`
	UseJudge      bool    `env:"JUDGE,default=false"`
	JudgeMinScore float64 `env:"JUDGE_MIN_SCORE,default=0.5"`

	ProjectID   string `env:"GOOGLE_CLOUD_PROJECT,required"`
	Location    string `env:"GOOGLE_CLOUD_LOCATION,default=us-east5"`
	GitHubToken string `env:"GITHUB_TOKEN,required"`
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	var cfg config
	if err := envconfig.Process(ctx, &cfg); err != nil {
		log.Error("load config", "err", err)
		os.Exit(1)
	}
	if cfg.Provider != "claude" && cfg.Provider != "gemini" {
		log.Error("invalid provider", "provider", cfg.Provider)
		os.Exit(1)
	}

	srv := &server{
		log: log,
		cfg: reviewer.RunnerConfig{
			Provider:      cfg.Provider,
			Model:         cfg.Model,
			ProjectID:     cfg.ProjectID,
			Location:      cfg.Location,
			GitHubToken:   cfg.GitHubToken,
			Clone:         cfg.Clone,
			Apply:         cfg.Apply,
			SkillsDir:     cfg.SkillsDir,
			UseJudge:      cfg.UseJudge,
			JudgeMinScore: cfg.JudgeMinScore,
		},
	}

	grpcServer := grpc.NewServer()
	workqueue.RegisterWorkqueueServiceServer(grpcServer, srv)
	healthgrpc.RegisterHealthServer(grpcServer, health.NewServer())

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		log.Error("listen", "err", err)
		os.Exit(1)
	}

	go func() {
		<-ctx.Done()
		log.Info("shutting down")
		grpcServer.GracefulStop()
	}()

	log.Info("reconciler ready", "port", cfg.Port, "provider", cfg.Provider)
	if err := grpcServer.Serve(lis); err != nil {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}

type server struct {
	workqueue.UnimplementedWorkqueueServiceServer
	log *slog.Logger
	cfg reviewer.RunnerConfig
}

// Process handles one key: a PR URL. A fresh review is run and posted. An
// error return tells the workqueue to retry (with backoff) up to max-retry
// times, after which the key goes to the dead-letter queue.
func (s *server) Process(ctx context.Context, req *workqueue.ProcessRequest) (*workqueue.ProcessResponse, error) {
	key := req.GetKey()
	owner, repo, pr, err := parsePRURL(key)
	if err != nil {
		s.log.Error("skip invalid key", "key", key, "err", err)
		// Don't retry malformed keys — they'd just loop until DLQ.
		return nil, status.Errorf(codes.InvalidArgument, "parse key %q: %v", key, err)
	}

	log := s.log.With("owner", owner, "repo", repo, "pr", pr)
	log.Info("processing PR")

	if err := reviewer.RunReview(ctx, log, s.cfg, owner, repo, pr); err != nil {
		log.Error("review failed", "err", err)
		return nil, err
	}

	return &workqueue.ProcessResponse{}, nil
}

// parsePRURL extracts (owner, repo, number) from a github.com PR URL emitted
// by the github-events CloudEvent bridge as the `pullrequesturl` extension.
// Example: https://github.com/octocat/hello-world/pull/42
func parsePRURL(raw string) (owner, repo string, pr int, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", 0, fmt.Errorf("parse URL: %w", err)
	}
	if u.Host != "github.com" {
		return "", "", 0, fmt.Errorf("unexpected host %q", u.Host)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "pull" {
		return "", "", 0, fmt.Errorf("not a PR URL: %s", u.Path)
	}
	n, err := strconv.Atoi(parts[3])
	if err != nil {
		return "", "", 0, fmt.Errorf("bad PR number %q", parts[3])
	}
	return parts[0], parts[1], n, nil
}
