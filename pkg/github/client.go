/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package github

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/go-github/v68/github"
	"golang.org/x/oauth2"
)

// Client wraps the GitHub API client for PR operations.
type Client struct {
	gh *github.Client
}

// NewClient creates a new GitHub client with the provided token.
func NewClient(ctx context.Context, token string) *Client {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(ctx, ts)
	return &Client{gh: github.NewClient(tc)}
}

// NewClientWithHTTP creates a GitHub client with a custom HTTP client.
func NewClientWithHTTP(httpClient *http.Client) *Client {
	return &Client{gh: github.NewClient(httpClient)}
}

// GetPR fetches the pull request metadata.
func (c *Client) GetPR(ctx context.Context, owner, repo string, number int) (*github.PullRequest, error) {
	pr, _, err := c.gh.PullRequests.Get(ctx, owner, repo, number)
	if err != nil {
		return nil, fmt.Errorf("get PR: %w", err)
	}
	return pr, nil
}

// GetPRDiff fetches the raw diff for a pull request.
func (c *Client) GetPRDiff(ctx context.Context, owner, repo string, number int) (string, error) {
	diff, _, err := c.gh.PullRequests.GetRaw(ctx, owner, repo, number, github.RawOptions{
		Type: github.Diff,
	})
	if err != nil {
		return "", fmt.Errorf("get PR diff: %w", err)
	}
	return diff, nil
}

// GetPRFiles fetches the list of files changed in a pull request.
func (c *Client) GetPRFiles(ctx context.Context, owner, repo string, number int) ([]*github.CommitFile, error) {
	opts := &github.ListOptions{PerPage: 100}
	var allFiles []*github.CommitFile

	for {
		files, resp, err := c.gh.PullRequests.ListFiles(ctx, owner, repo, number, opts)
		if err != nil {
			return nil, fmt.Errorf("list PR files: %w", err)
		}
		allFiles = append(allFiles, files...)

		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	return allFiles, nil
}

// GetFileContent fetches the content of a file at a specific commit SHA.
func (c *Client) GetFileContent(ctx context.Context, owner, repo, path, ref string) (string, error) {
	content, _, _, err := c.gh.Repositories.GetContents(ctx, owner, repo, path, &github.RepositoryContentGetOptions{
		Ref: ref,
	})
	if err != nil {
		return "", fmt.Errorf("get file content: %w", err)
	}

	if content == nil {
		return "", fmt.Errorf("file %s not found", path)
	}

	decoded, err := content.GetContent()
	if err != nil {
		return "", fmt.Errorf("decode file content: %w", err)
	}

	return decoded, nil
}

// CreateReview submits a review to a pull request.
func (c *Client) CreateReview(ctx context.Context, owner, repo string, number int, review *github.PullRequestReviewRequest) (*github.PullRequestReview, error) {
	created, _, err := c.gh.PullRequests.CreateReview(ctx, owner, repo, number, review)
	if err != nil {
		return nil, fmt.Errorf("create review: %w", err)
	}
	return created, nil
}

// Ptr is a helper to get a pointer to a value.
func Ptr[T any](v T) *T {
	return &v
}
