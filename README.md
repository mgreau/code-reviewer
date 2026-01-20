# Code Reviewer

A CLI tool that uses AI to review GitHub Pull Requests. Supports both **Claude** and **Gemini** models via **Google Cloud Vertex AI**. Built with the [Driftless](../mono/driftless) framework for structured AI agent execution.

## Features

- Fetches PR diff and changed files from GitHub
- Reviews code using Claude or Gemini for general quality issues:
  - Bugs and logic errors
  - Code style and best practices
  - Missing error handling
  - Security vulnerabilities
  - Readability and maintainability
- Posts review comments with suggestions back to the PR
- Supports dry-run mode to preview reviews without posting
- **Both providers use Vertex AI** - single authentication method

## Installation

```bash
go install github.com/example/code-reviewer/cmd/reviewer@latest
```

Or build from source:

```bash
go build -o reviewer ./cmd/reviewer
```

## Usage

### Prerequisites

1. A Google Cloud project with Vertex AI API enabled
2. Application Default Credentials configured
3. A GitHub token with repo access

### Environment Variables

```bash
# Required: Google Cloud project for Vertex AI
export GOOGLE_CLOUD_PROJECT=your-gcp-project-id

# Optional: Vertex AI location (defaults to us-east5 for Claude model support)
export GOOGLE_CLOUD_LOCATION=us-east5

# Required: GitHub token for PR access
export GITHUB_TOKEN=your-github-token

# Set up Application Default Credentials
gcloud auth application-default login
```

### Running a Review

```bash
# Review using Claude (default)
reviewer -owner=myorg -repo=myrepo -pr=123

# Review using Gemini
reviewer -owner=myorg -repo=myrepo -pr=123 -provider=gemini

# Preview the review without posting to GitHub
reviewer -owner=myorg -repo=myrepo -pr=123 -dry-run
```

### Command Line Flags

| Flag | Description | Required | Default |
|------|-------------|----------|---------|
| `-owner` | Repository owner (user or organization) | Yes | - |
| `-repo` | Repository name | Yes | - |
| `-pr` | Pull request number | Yes | - |
| `-provider` | AI provider: `claude` or `gemini` | No | `claude` |
| `-dry-run` | Preview review without posting to GitHub | No | `false` |
| `-judge` | Enable AI judge to filter low-quality suggestions | No | `false` |
| `-judge-model` | Model to use for judging | No | `gemini-2.5-flash` |
| `-judge-min-score` | Minimum score (0.0-1.0) to include a suggestion | No | `0.5` |

## AI Judge

The code-reviewer includes an optional AI judge that evaluates each suggestion for quality before posting. The judge evaluates suggestions on:

- **Accuracy**: Is the issue correctly identified?
- **Actionability**: Is the suggestion specific enough to act on?
- **Value**: Does this issue matter for code quality?
- **Clarity**: Is the message clear and easy to understand?

### Using the Judge

```bash
# Enable judge with default settings (min score 0.5)
reviewer -owner=myorg -repo=myrepo -pr=123 -judge

# Use a higher threshold to only include high-quality suggestions
reviewer -owner=myorg -repo=myrepo -pr=123 -judge -judge-min-score=0.7

# Use a different model for judging
reviewer -owner=myorg -repo=myrepo -pr=123 -judge -judge-model=gemini-2.5-flash
```

### Judge Output

When the judge is enabled, each suggestion includes:
- **Score**: 0.0 to 1.0 rating of suggestion quality
- **Reasoning**: Explanation of the score
- Suggestions below the threshold are filtered out

## Architecture

```
code-reviewer/
├── cmd/reviewer/main.go         # CLI entry point
├── pkg/
│   ├── github/client.go         # GitHub API wrapper
│   └── reviewer/
│       ├── types.go             # ReviewResult, CodeSuggestion
│       ├── prompt.go            # Review prompt template
│       └── reviewer.go          # Review orchestration (Claude + Gemini)
├── go.mod
└── README.md
```

## Provider Comparison

Both providers use **Vertex AI** for authentication and API access.

| Feature | Claude | Gemini |
|---------|--------|--------|
| Model | claude-opus-4-5@20251101 | gemini-2.5-flash |
| Backend | Vertex AI | Vertex AI |
| Auth | Application Default Credentials | Application Default Credentials |

## Driftless Integration

This tool uses the following Driftless packages:

| Package | Usage |
|---------|-------|
| `executor/claudeexecutor` | Claude conversation orchestration |
| `executor/googleexecutor` | Gemini conversation orchestration |
| `promptbuilder` | Safe prompt binding with XML/JSON |
| `submitresult` | Structured result submission |
| `toolcall/claudetool` | Claude tool parameter extraction |
| `toolcall/googletool` | Gemini tool parameter extraction |
| `judge` | AI judge for evaluating suggestion quality |

## Development

### Prerequisites

- Go 1.25.4 or later
- Access to the driftless package (via replace directive in go.mod)
- Google Cloud project with Vertex AI enabled

### Building

```bash
go build ./cmd/reviewer
```

### Testing

```bash
go test ./...
```

## License

Apache-2.0
