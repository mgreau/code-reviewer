/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package reviewer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"chainguard.dev/driftless/pkg/judge"
)

// JudgeConfig contains configuration for the judge evaluation.
type JudgeConfig struct {
	// Enabled determines whether judge evaluation is performed.
	Enabled bool

	// Model is the model to use for judging (e.g., "claude-sonnet-4-5@20251101").
	Model string

	// MinScore is the minimum score (0.0-1.0) for a suggestion to be included.
	// Suggestions below this threshold are filtered out.
	MinScore float64
}

// DefaultJudgeConfig returns the default judge configuration.
func DefaultJudgeConfig() JudgeConfig {
	return JudgeConfig{
		Enabled:  false,
		Model:    "gemini-2.5-flash", // Use a fast model for judging
		MinScore: 0.5,                // Filter out suggestions scoring below 0.5
	}
}

// JudgedSuggestion contains a suggestion along with its judge evaluation.
type JudgedSuggestion struct {
	Suggestion  CodeSuggestion
	Score       float64
	Reasoning   string
	Improvement []string
}

// judgeCriterion defines what makes a good code review suggestion.
const judgeCriterion = `Evaluate this code review suggestion on the following criteria:

1. **Accuracy**: Is the issue correctly identified? Is the technical assessment correct?
2. **Actionability**: Is the suggestion specific enough to act on? Does it provide clear guidance?
3. **Value**: Does this issue matter? Is it worth the developer's time to address?
4. **Clarity**: Is the message clear and easy to understand?

A score of 1.0 means the suggestion is excellent: accurate, actionable, valuable, and clear.
A score of 0.5 means the suggestion is mediocre: partially correct or not very actionable.
A score of 0.0 means the suggestion is poor: incorrect, unclear, or not valuable.

Consider whether this is something a senior developer would flag in a real code review.`

// JudgeSuggestions evaluates code review suggestions using an AI judge.
// Returns the suggestions with their judge scores, filtered by MinScore.
func JudgeSuggestions(ctx context.Context, projectID, location string, config JudgeConfig, suggestions []CodeSuggestion) ([]JudgedSuggestion, error) {
	if !config.Enabled || len(suggestions) == 0 {
		// Return all suggestions without judging
		result := make([]JudgedSuggestion, len(suggestions))
		for i, s := range suggestions {
			result[i] = JudgedSuggestion{
				Suggestion: s,
				Score:      1.0, // Assume perfect score when not judging
			}
		}
		return result, nil
	}

	log := slog.With("component", "judge", "model", config.Model)
	log.Info("creating judge instance")

	// Create judge instance
	j, err := judge.NewVertex(ctx, projectID, location, config.Model)
	if err != nil {
		return nil, fmt.Errorf("create judge: %w", err)
	}

	log.Info("evaluating suggestions", "count", len(suggestions), "min_score", config.MinScore)

	var result []JudgedSuggestion
	var filtered int

	for i, s := range suggestions {
		// Format the suggestion for evaluation
		suggestionJSON, err := json.MarshalIndent(s, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("marshal suggestion %d: %w", i, err)
		}

		// Create judge request
		req := &judge.Request{
			Mode:         judge.StandaloneMode,
			ActualAnswer: string(suggestionJSON),
			Criterion:    judgeCriterion,
		}

		// Execute judgment
		judgement, err := j.Judge(ctx, req)
		if err != nil {
			log.Warn("judge evaluation failed, including suggestion anyway",
				"suggestion", i,
				"file", s.File,
				"error", err)
			// Include suggestion on judge failure
			result = append(result, JudgedSuggestion{
				Suggestion: s,
				Score:      1.0,
				Reasoning:  "Judge evaluation failed",
			})
			continue
		}

		log.Info("suggestion evaluated",
			"index", i+1,
			"file", s.File,
			"line", s.LineEnd,
			"severity", s.Severity,
			"score", judgement.Score)

		// Filter by score threshold
		if judgement.Score < config.MinScore {
			filtered++
			log.Info("filtering low-quality suggestion",
				"file", s.File,
				"score", judgement.Score,
				"threshold", config.MinScore,
				"reasoning", judgement.Reasoning)
			continue
		}

		result = append(result, JudgedSuggestion{
			Suggestion:  s,
			Score:       judgement.Score,
			Reasoning:   judgement.Reasoning,
			Improvement: judgement.Suggestions,
		})
	}

	log.Info("judge evaluation complete",
		"total", len(suggestions),
		"passed", len(result),
		"filtered", filtered)

	return result, nil
}

// ExtractSuggestions extracts CodeSuggestions from JudgedSuggestions.
func ExtractSuggestions(judged []JudgedSuggestion) []CodeSuggestion {
	result := make([]CodeSuggestion, len(judged))
	for i, j := range judged {
		result[i] = j.Suggestion
	}
	return result
}
