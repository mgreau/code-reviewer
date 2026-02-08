# Test Coverage Analysis

## Current State

The project has **zero test files** and **0% test coverage**. There are no `*_test.go` files anywhere in the codebase.

The codebase consists of ~1,145 lines across 5 source files:

| File | Lines | Description |
|------|-------|-------------|
| `cmd/reviewer/main.go` | 148 | CLI entry point |
| `pkg/github/client.go` | 121 | GitHub API wrapper |
| `pkg/reviewer/types.go` | 73 | Type definitions and helpers |
| `pkg/reviewer/reviewer.go` | 536 | Core review orchestration |
| `pkg/reviewer/prompt.go` | 106 | Prompt template and binding |
| `pkg/reviewer/judge.go` | 161 | AI judge for quality filtering |

---

## Proposed Test Plan

Tests are organized into three tiers by priority, based on risk, complexity, and ease of testing.

### Tier 1 -- High Priority (Pure functions with complex logic)

These are the most valuable tests to write first: they cover critical logic, have no external dependencies, and are straightforward to test with table-driven tests.

#### 1. Diff Parsing (`newDiffLines`, `contains`, `parseHunkStart`)

**File:** `pkg/reviewer/reviewer.go:418-521`

**Why this matters:** This is the most critical logic in the codebase. It determines whether review comments can be posted as inline comments on the correct lines. Bugs here silently cause comments to be dropped or posted on wrong lines -- neither of which produces an error.

**Test cases to cover:**
- Simple single-file diff with one hunk
- Multi-file diff with multiple hunks per file
- Diff with only additions (new file)
- Diff with only deletions (deleted file)
- Diff with mixed additions, deletions, and context lines
- Hunk with `\ No newline at end of file` marker
- Edge case: line number at exact boundary of a range (start and end)
- Edge case: line number outside all ranges
- Edge case: file not present in diff
- Edge case: empty diff string
- `parseHunkStart` with various hunk header formats: `@@ -1,5 +1,7 @@`, `@@ -0,0 +1,10 @@`, `@@ -10 +10 @@` (no count), malformed headers

```go
func TestNewDiffLines(t *testing.T) {
    // Table-driven tests with realistic unified diffs
}

func TestParseHunkStart(t *testing.T) {
    // Table-driven tests for hunk header parsing
}

func TestDiffLinesContains(t *testing.T) {
    // Boundary and lookup tests
}
```

#### 2. Code Extraction from Suggestions (`extractCodeFromSuggestion`)

**File:** `pkg/reviewer/reviewer.go:338-380`

**Why this matters:** AI models sometimes return markdown-wrapped code in the suggestion field despite instructions not to. This function is the safety net that strips markdown fences. If it fails, GitHub receives raw markdown instead of code in suggestion blocks.

**Test cases to cover:**
- Plain code with no markdown fences (passthrough)
- Single code block with language identifier (` ```go\n...\n``` `)
- Single code block without language identifier
- Multiple code blocks (should return last one)
- Nested or malformed code fences
- Empty string input
- Code fences with no content between them
- Suggestion with ``` appearing in non-fence context (e.g., in a comment)

#### 3. Review Comment Construction (`buildReviewComment`)

**File:** `pkg/reviewer/reviewer.go:383-405`

**Why this matters:** This constructs the GitHub API payload for inline comments. Incorrect construction leads to API errors or misplaced comments.

**Test cases to cover:**
- Single-line comment (`LineStart == LineEnd`)
- Multi-line comment (`LineStart != LineEnd`, both in diff)
- Multi-line comment where `LineStart` is NOT in diff (should omit `StartLine`)
- Comment with suggestion text
- Comment without suggestion text
- Comment with suggestion containing markdown (exercises `extractCodeFromSuggestion` integration)
- Each severity level formatting

### Tier 2 -- Medium Priority (Simple pure functions + mock-based integration)

#### 4. Type Helper Methods

**File:** `pkg/reviewer/types.go:25-57`

**Why these matter:** Foundation methods used throughout. Simple but worth covering to prevent regressions.

**Test cases:**
- `HasErrors()`: empty suggestions, no errors, one error among many, all errors
- `IsError()`: exact match, case variations ("Error", "ERROR", "error"), non-matching
- `IsWarning()`: same case patterns as `IsError`
- `NormalizedSeverity()`: lowercase, mixed case, empty string

#### 5. File Formatting (`formatFiles`)

**File:** `pkg/reviewer/reviewer.go:524-535`

**Test cases:**
- Empty file list
- Single file
- Multiple files with different statuses (added, modified, removed)
- File with zero additions/deletions

#### 6. Prompt Binding (`ReviewRequest.Bind`)

**File:** `pkg/reviewer/prompt.go:85-106`

**Why this matters:** If binding fails or produces malformed XML, the AI model receives a broken prompt. This is testable without external services since `promptbuilder` is a local dependency.

**Test cases:**
- Normal binding with all fields populated
- Binding with empty fields
- Binding with special XML characters in description (e.g., `<`, `>`, `&`) to verify CDATA handling
- Binding with very large diff content

#### 7. `PostReview` Orchestration

**File:** `pkg/reviewer/reviewer.go:263-333`

**Why this matters:** This is the function that actually posts to GitHub. Testing with a mock `PRClient` ensures the correct API call structure.

**Approach:** The `Reviewer` struct holds `github` as `*ghclient.Client` (concrete type), not the `PRClient` interface. To test this properly, refactor `Reviewer` to accept the `PRClient` interface instead. The interface already exists (`pkg/github/client.go:19-25`) but isn't used by `Reviewer`.

**Test cases (with mock):**
- Posts APPROVE event when `result.Approved == true`
- Posts COMMENT event when `result.Approved == false`
- Inline comments are created for suggestions within diff ranges
- Out-of-diff suggestions appear in the review body text
- Returns error when GitHub client is nil
- Returns error when `CreateReview` fails

#### 8. `Review` Orchestration

**File:** `pkg/reviewer/reviewer.go:100-160`

**Same refactoring prerequisite as above.** Additionally needs the executor to be injectable for testing.

**Test cases (with mocks):**
- Returns error when GitHub client is nil
- Returns error when `GetPR` fails
- Returns error when `GetPRDiff` fails
- Returns error when `GetPRFiles` fails
- Returns error for unknown provider
- Successful flow returns correct `ReviewOutput` structure

### Tier 3 -- Lower Priority (External service integration)

#### 9. Judge Functions

**File:** `pkg/reviewer/judge.go`

**`JudgeSuggestions` with judge disabled (no external calls):**
- Returns all suggestions with score 1.0 when `Enabled == false`
- Returns empty slice for empty input

**`ExtractSuggestions`:**
- Correct extraction from judged suggestions
- Empty input returns empty output

**`DefaultJudgeConfig`:**
- Returns expected default values

**`JudgeSuggestions` with judge enabled:** Requires mocking `judge.NewVertex`, which currently uses a package-level constructor. Would need a refactor to inject the judge dependency.

#### 10. GitHub Client (`pkg/github/client.go`)

**Approach:** Use `net/http/httptest` to create a mock GitHub API server.

**Test cases:**
- `GetPR`: successful fetch, 404 error
- `GetPRDiff`: successful diff retrieval
- `GetPRFiles`: single page, pagination across multiple pages
- `GetFileContent`: successful read, file not found, nil content
- `CreateReview`: successful creation, API error
- `Ptr`: returns pointer to value

#### 11. CLI (`cmd/reviewer/main.go`)

Lowest priority. The CLI is a thin orchestration layer. If the underlying packages are well-tested, the CLI is low risk.

**Possible approach:** Extract the core logic from `main()` into a `run(args []string) error` function, then test flag parsing and validation:
- Missing required flags
- Invalid provider value
- Missing environment variables

---

## Recommended Refactoring for Testability

### 1. Use `PRClient` interface in `Reviewer` struct

The `PRClient` interface already exists but `Reviewer` takes `*ghclient.Client` (concrete). Change:

```go
// Current
type Reviewer struct {
    github     *ghclient.Client
    // ...
}

// Proposed
type Reviewer struct {
    github     ghclient.PRClient
    // ...
}
```

This unlocks mock-based testing for `Review()` and `PostReview()` without any HTTP test servers.

### 2. Make executor interfaces injectable

Currently `Reviewer` holds concrete executor types. For testing the review orchestration without calling Vertex AI, consider adding a constructor that accepts pre-built executors:

```go
func NewWithExecutors(claudeExec claudeexecutor.Interface[...], googleExec googleexecutor.Interface[...]) *Reviewer
```

### 3. Extract `main()` logic into testable function

```go
func run(ctx context.Context, args []string, env map[string]string) error {
    // current main() logic
}
```

---

## Summary: Effort vs. Impact

| Test Area | Effort | Impact | External Deps |
|-----------|--------|--------|---------------|
| Diff parsing | Low | **Critical** | None |
| Code extraction | Low | High | None |
| Comment construction | Low | High | None |
| Type helpers | Low | Medium | None |
| File formatting | Low | Low | None |
| Prompt binding | Low | Medium | Driftless (local) |
| PostReview (with mock) | Medium | High | Refactor needed |
| Review (with mock) | Medium | High | Refactor needed |
| Judge (disabled path) | Low | Medium | None |
| GitHub client (httptest) | Medium | Medium | None |
| CLI | Medium | Low | Refactor needed |

**Recommended starting point:** Tier 1 tests (diff parsing, code extraction, comment construction). These cover the most critical and bug-prone logic, require zero refactoring, have no external dependencies, and can be written entirely as table-driven tests.
