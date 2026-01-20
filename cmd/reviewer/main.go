/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	ghclient "github.com/example/code-reviewer/pkg/github"
	"github.com/example/code-reviewer/pkg/reviewer"
)

func main() {
	// Parse command line flags
	owner := flag.String("owner", "", "Repository owner (required)")
	repo := flag.String("repo", "", "Repository name (required)")
	pr := flag.Int("pr", 0, "Pull request number (required)")
	provider := flag.String("provider", "claude", "AI provider: claude or gemini")
	dryRun := flag.Bool("dry-run", false, "Print review without posting to GitHub")
	useJudge := flag.Bool("judge", false, "Use AI judge to filter low-quality suggestions")
	judgeModel := flag.String("judge-model", "gemini-2.5-flash", "Model to use for judging")
	judgeMinScore := flag.Float64("judge-min-score", 0.5, "Minimum judge score (0.0-1.0) to include a suggestion")
	flag.Parse()

	// Validate required flags
	if *owner == "" || *repo == "" || *pr == 0 {
		fmt.Fprintln(os.Stderr, "Usage: reviewer -owner=<owner> -repo=<repo> -pr=<number> [-provider=claude|gemini]")
		flag.PrintDefaults()
		os.Exit(1)
	}

	// Validate provider
	if *provider != "claude" && *provider != "gemini" {
		log.Fatalf("Invalid provider %q: must be 'claude' or 'gemini'", *provider)
	}

	// Get Vertex AI configuration (required for both providers)
	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if projectID == "" {
		log.Fatal("GOOGLE_CLOUD_PROJECT environment variable is required")
	}
	location := os.Getenv("GOOGLE_CLOUD_LOCATION")
	if location == "" {
		location = "us-east5"
	}

	// Check for GitHub token
	githubToken := os.Getenv("GITHUB_TOKEN")
	if githubToken == "" {
		log.Fatal("GITHUB_TOKEN environment variable is required")
	}

	ctx := context.Background()

	// Create the reviewer based on provider (both use Vertex AI)
	var rev *reviewer.Reviewer
	var err error

	switch *provider {
	case "claude":
		rev, err = reviewer.NewWithClaude(ctx, projectID, location)
	case "gemini":
		rev, err = reviewer.NewWithGemini(ctx, projectID, location)
	}

	if err != nil {
		log.Fatalf("Failed to create reviewer: %v", err)
	}

	// Create GitHub client and set it
	githubClient := ghclient.NewClient(ctx, githubToken)
	rev.SetGitHub(githubClient)

	fmt.Printf("Reviewing PR %s/%s#%d using %s (via Vertex AI)...\n", *owner, *repo, *pr, *provider)

	// Perform the review
	output, err := rev.Review(ctx, *owner, *repo, *pr)
	if err != nil {
		log.Fatalf("Review failed: %v", err)
	}

	result := output.Result

	// Print the review summary
	fmt.Println("\n=== Review Summary ===")
	fmt.Println(result.Summary)
	fmt.Printf("\nApproved: %v\n", result.Approved)
	fmt.Printf("Suggestions: %d\n", len(result.Suggestions))

	// Apply judge if enabled
	judgeConfig := reviewer.JudgeConfig{
		Enabled:  *useJudge,
		Model:    *judgeModel,
		MinScore: *judgeMinScore,
	}

	judgedSuggestions, err := reviewer.JudgeSuggestions(ctx, projectID, location, judgeConfig, result.Suggestions)
	if err != nil {
		log.Fatalf("Judge evaluation failed: %v", err)
	}

	// Update result with filtered suggestions
	if *useJudge {
		originalCount := len(result.Suggestions)
		result.Suggestions = reviewer.ExtractSuggestions(judgedSuggestions)
		if originalCount != len(result.Suggestions) {
			fmt.Printf("\nJudge filtered %d low-quality suggestions (threshold: %.2f)\n",
				originalCount-len(result.Suggestions), *judgeMinScore)
		}
	}

	// Print suggestions
	if len(judgedSuggestions) > 0 {
		fmt.Println("\n=== Suggestions ===")
		for i, js := range judgedSuggestions {
			s := js.Suggestion
			if *useJudge {
				fmt.Printf("\n[%d] %s:%d-%d (%s) [score: %.2f]\n", i+1, s.File, s.LineStart, s.LineEnd, s.Severity, js.Score)
			} else {
				fmt.Printf("\n[%d] %s:%d-%d (%s)\n", i+1, s.File, s.LineStart, s.LineEnd, s.Severity)
			}
			fmt.Printf("    %s\n", s.Message)
			if s.Suggestion != "" {
				fmt.Printf("    Suggestion: %s\n", s.Suggestion)
			}
			if *useJudge && js.Reasoning != "" {
				fmt.Printf("    Judge reasoning: %s\n", js.Reasoning)
			}
		}
	}

	// Post to GitHub unless dry-run
	if !*dryRun {
		fmt.Println("\nPosting review to GitHub...")
		if err := rev.PostReview(ctx, *owner, *repo, *pr, output); err != nil {
			log.Fatalf("Failed to post review: %v", err)
		}
		fmt.Println("Review posted successfully!")
	} else {
		fmt.Println("\nDry-run mode: Review not posted to GitHub")
	}
}
