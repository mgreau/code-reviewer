# Code Reviewer

A Go service that reviews GitHub Pull Requests with Claude or Gemini. Ships in **three architectures**, all sharing the same review core:

1. **One-shot CLI** (`reviewer review`) — for manual runs and CI jobs.
2. **Webhook server** (`reviewer serve`) — long-running HTTP process for local dev and small teams.
3. **Cloud Run reconciler** (`cmd/reconciler` + `deploy/terraform`) — workqueue-backed service for team- and org-scale production.

Built on the [driftlessaf](https://github.com/driftlessaf/go-driftlessaf) agent framework. Mirrors the capabilities of [vercel-labs/openreview](https://github.com/vercel-labs/openreview) — progressive skills, full-repo tool access via a local workdir, mention-triggered reviews, reply comments — while staying a standalone Go binary with no Vercel/Next.js dependencies.

## Contents

- [Features](#features)
- [Operating Modes](#operating-modes)
- [How It Works](#how-it-works)
- [Installation](#installation)
- [Environment](#environment)
- [`reviewer review` (one-shot CLI)](#reviewer-review-one-shot-cli)
- [`reviewer serve` (webhook server)](#reviewer-serve-webhook-server)
- [Cloud Run reconciler (production)](#cloud-run-reconciler-production)
- [Skills](#skills)
- [Workdir, Tools, and `-apply`](#workdir-tools-and--apply)
- [AI Judge](#ai-judge)
- [Feature Parity with openreview](#feature-parity-with-openreview)
- [Architecture](#architecture)
- [Development](#development)

## Features

- Three operating modes sharing one review engine: one-shot `review`, long-running `serve` webhook, and a workqueue-backed Cloud Run reconciler for production.
- Claude and Gemini backends, both via Google Cloud Vertex AI (single auth path).
- Structured review output: summary, inline suggestions, approval flag.
- Optional local workdir (reuse an existing checkout or shallow-clone the PR branch) for full-repo code access.
- Agent tools: `read_file`, `bash`, `write_file`, `load_skill`, `reply`, `submit_result` (availability depends on mode — see [Workdir, Tools, and `-apply`](#workdir-tools-and--apply)).
- Progressive skills loaded from `.agents/skills/<name>/SKILL.md` — mirrors openreview's skill system.
- Optional `-apply` step commits and pushes agent-made file edits back to the PR branch.
- Optional AI judge filters low-quality suggestions before posting.
- Webhook `serve` mode: HMAC-verified GitHub `issue_comment` events, `@<bot-login>` mention trigger, eyes-reaction ack, failure comment on error.
- Comment command grammar on top of `@bot` mentions: `apply N`, `apply all`, `skip N`, `rereview` — see [Comment command grammar](#comment-command-grammar).

## Operating Modes

All three modes invoke the same review engine (`pkg/reviewer` — `RunReview` / `Review` / `PostReview`). What differs is how a review is **triggered**, how it's **hosted**, and what happens when **multiple comments arrive at once**.

### 1. One-shot CLI — `reviewer review`

```
  You / CI
     │
     │  reviewer review -owner=X -repo=Y -pr=N
     ▼
  ┌───────────────────────────────────────┐
  │  reviewer (one-shot process)          │
  │                                        │
  │  fetch PR                              │
  │  (optional) clone workdir              │
  │  run review via Vertex AI              │
  │  (optional) judge low-quality filter   │
  │  (optional) apply: commit + push       │
  │  post review  ───────────────────────┐ │
  │                                      │ │
  │  process exits when done             │ │
  └──────────────────────────────────────┼─┘
                                         ▼
                                     GitHub API
```

No hosting, no webhook, no queue. You run it — it runs — it exits. Ideal for **manual reviews** and **CI jobs that want a review on every PR** without waiting for a human to mention the bot.

### 2. Webhook server — `reviewer serve`

```
     GitHub webhook
        │  POST /webhook + HMAC signature
        ▼
  ┌──────────────────────────────────────────┐
  │  reviewer serve  (long-lived HTTP proc)  │
  │                                           │
  │    HMAC verify ─▶ parse @bot command     │
  │                          │                │
  │                          ▼                │
  │   ◄── 202 Accepted ── goroutine           │
  │                          │                │
  │                          ▼                │
  │                  RunReview / Apply / Skip │
  └──────────────────────────┼────────────────┘
                             ▼
                         GitHub API  (post review / reply / push)

  no queue · no dedupe · no retry budget · no DLQ
  one process · scales vertically only
  supports review, apply, skip, rereview verbs
```

Single binary, single process. Put it behind [smee.io](https://smee.io/), [ngrok](https://ngrok.com/), or a reverse proxy and it responds to `@bot` mentions in real time. **Best for local development** (iterate on prompts and skills against a test repo) and **small teams** where a dropped webhook occasionally is tolerable.

### 3. Cloud Run reconciler — `cmd/reconciler` + `deploy/terraform`

```
                         ┌─ Cloud Run ────┐
     GitHub ──POST─────▶ │ github-events  │
     webhook             │ (HMAC verify,  │
                         │  public URL)   │
                         └────────┬───────┘
                                  │  CloudEvent
                                  ▼
                            Pub/Sub broker
                                  │
                                  ▼
                         ┌─ Cloud Run ────┐
                         │ bridge         │  key = pullrequesturl
                         │ (workqueue     │
                         │  dispatcher)   │
                         └────────┬───────┘
                                  │  dedupe · retry · DLQ
                                  ▼
                         ┌─ Cloud Run ────┐
                         │ reconciler     │─▶ RunReview ─▶ GitHub API
                         │ (cmd/          │
                         │  reconciler)   │
                         └────────────────┘

  keyed by PR URL — a burst of @bot comments collapses to one review
  5 Terraform modules + ko build (no Dockerfile, no DNS, no load balancer)
  review verb only today (apply/skip = follow-up)
```

Purpose-built for **team- and org-scale**: a burst of `@bot` comments on the same PR dedupes to one work item, transient failures retry with backoff, exhausted keys land in a DLQ for inspection, and the reconciler scales horizontally via the workqueue dispatcher.

### Picking a mode

|  | `reviewer review` | `reviewer serve` | Cloud Run reconciler |
|---|---|---|---|
| **Shape** | One-shot CLI | HTTP webhook server | Workqueue-driven service |
| **Primary use case** | Manual runs, CI jobs | Local dev, small teams | Team / org production |
| **Trigger** | CLI invocation | GitHub `issue_comment` webhook | GitHub `issue_comment` webhook |
| **Hosting** | None — you run it | Single binary on a VM / container | Cloud Run on GCP |
| **Deploy** | `go install` | `go install` + reverse proxy or tunnel | `terraform apply` (one command) |
| **Dedupe by PR** | N/A | ❌ | ✅ |
| **Retry on failure** | N/A | ❌ | ✅ backoff + DLQ |
| **Horizontal scaling** | N/A | ❌ | ✅ dispatcher replicas |
| **Verbs supported** | `review` only | `review`, `apply`, `skip`, `rereview` | `review` (others = follow-up) |
| **GCP footprint** | none | none | Cloud Run, Pub/Sub, Secret Manager, GCS, Artifact Registry, Vertex AI |

The legacy flat-flag shape (`reviewer -owner=... -repo=... -pr=...`) still works and is routed to `review`.

## How It Works

The diagram below is the **review flow shared by all three modes**. What differs is only the trigger box at the top (CLI invocation vs webhook → goroutine vs webhook → workqueue → reconciler); everything from step 2 down is the same code path through `pkg/reviewer`.

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                          CODE-REVIEWER                                       │
│                                                                              │
│   reviewer review  -owner=... -repo=... -pr=...    (one-shot CLI)           │
│   reviewer serve   -bot-login=openreview ...       (webhook server)         │
│   cmd/reconciler                                   (Cloud Run reconciler)   │
└─────────────────────────────────────────────────────────────────────────────┘
                                   │
                                   ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                       1. TRIGGER + CONTEXT                                   │
│                                                                              │
│   CLI: flags → fetch PR                                                      │
│   Webhook: HMAC-verify → parse issue_comment → match @mention → ack 👀      │
│   Reconciler: workqueue key=PR URL → RunReview(owner, repo, pr)              │
│                                                                              │
│   Optional: shallow-clone PR branch into a workdir                           │
│             discover .agents/skills/*/SKILL.md                               │
│             load prior PR comments as conversation                           │
└─────────────────────────────────────────────────────────────────────────────┘
                                   │
                                   ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                   2. AI REVIEW (Driftless agent loop)                        │
│                                                                              │
│   promptbuilder  ─►  claudeexecutor │ googleexecutor  (Vertex AI)            │
│                                                                              │
│   Tools the agent can call:                                                  │
│     read_file     — fetch any changed file (always)                          │
│     bash          — shell in workdir          (clone/workdir only)           │
│     write_file    — edit files in workdir     (clone/workdir only)           │
│     load_skill    — read a progressive SKILL  (when skills discovered)       │
│     reply         — post an issue comment     (serve mode)                   │
│     submit_result — return structured JSON    (always; terminates the loop)  │
└─────────────────────────────────────────────────────────────────────────────┘
                                   │
                                   ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                   3. JUDGE (optional)                                        │
│   Score each suggestion on accuracy/actionability/value/clarity;             │
│   drop anything below -judge-min-score.                                      │
└─────────────────────────────────────────────────────────────────────────────┘
                                   │
                                   ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                   4. APPLY + POST                                            │
│                                                                              │
│   -apply (optional): commit & push any agent-made file edits                 │
│                                                                              │
│   Inline comments on diff-covered lines + review body with the rest          │
│   GitHub event: APPROVE if result.approved, else COMMENT                     │
│   On failure: post a "Review failed" comment (serve mode)                    │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Installation

```bash
go install github.com/example/code-reviewer/cmd/reviewer@latest
```

Or build from source:

```bash
go build -o reviewer ./cmd/reviewer
```

## Environment

Required for both modes:

```bash
export GOOGLE_CLOUD_PROJECT=your-gcp-project-id
export GITHUB_TOKEN=ghp_...        # PAT or app installation token with repo access
gcloud auth application-default login
```

Optional:

```bash
export GOOGLE_CLOUD_LOCATION=us-east5             # default; Claude-supporting region
export GITHUB_WEBHOOK_SECRET=...                  # serve mode only; verifies X-Hub-Signature-256
```

When `GITHUB_WEBHOOK_SECRET` is empty, `serve` accepts unsigned webhooks — use only for local dev.

## `reviewer review` (one-shot CLI)

```bash
# Simplest: review a PR and post inline comments
reviewer review -owner=myorg -repo=myrepo -pr=123

# Dry-run (prints what would be posted)
reviewer review -owner=myorg -repo=myrepo -pr=123 -dry-run

# Shallow-clone the PR branch to give the agent bash/write_file access
reviewer review -owner=myorg -repo=myrepo -pr=123 -clone

# Clone + let the agent commit and push any fixes it made
reviewer review -owner=myorg -repo=myrepo -pr=123 -clone -apply

# Point at an existing local checkout instead of cloning
reviewer review -owner=myorg -repo=myrepo -pr=123 -workdir=/path/to/checkout

# Use Gemini, or override the model
reviewer review -owner=myorg -repo=myrepo -pr=123 -provider=gemini
reviewer review -owner=myorg -repo=myrepo -pr=123 -model=claude-sonnet-4-6@20251022
```

### Flags

| Flag | Description | Default |
|---|---|---|
| `-owner` | Repo owner (required) | — |
| `-repo` | Repo name (required) | — |
| `-pr` | PR number (required) | — |
| `-provider` | `claude` or `gemini` | `claude` |
| `-model` | Model override (empty = provider default) | `""` |
| `-dry-run` | Print review, skip GitHub post | `false` |
| `-clone` | Shallow-clone the PR branch into a temp workdir | `false` |
| `-workdir` | Use an existing local checkout (mutually exclusive with `-clone`) | `""` |
| `-apply` | After review, commit & push agent-made file changes (requires `-clone`/`-workdir`) | `false` |
| `-skills` | Fallback skills directory scanned when `-clone`/`-workdir` is off | `.agents/skills` |
| `-judge` | Filter suggestions via a second-pass AI judge | `false` |
| `-judge-model` | Model used for judging | `gemini-2.5-flash` |
| `-judge-min-score` | Drop suggestions below this score (0.0–1.0) | `0.5` |

## `reviewer serve` (webhook server)

Long-running HTTP server that listens for GitHub `issue_comment` events and runs a review whenever a PR comment `@mentions` the configured bot login.

```bash
reviewer serve \
  -addr=:8080 \
  -bot-login=openreview \
  -provider=claude \
  -clone \
  -apply \
  -judge
```

### Endpoints

| Path | Purpose |
|---|---|
| `GET /healthz` | Liveness probe — returns `ok` |
| `POST /webhook` | GitHub webhook receiver |

### Request lifecycle

1. Verify `X-Hub-Signature-256` against `GITHUB_WEBHOOK_SECRET` (bypassed when secret is empty).
2. Dispatch by `X-GitHub-Event`:
   - `ping` → `pong`
   - `issue_comment` with `action=created` **and** a `pull_request` pointer **and** a body that matches `@<bot-login>` → parse the command and enqueue work.
   - Anything else → `204 No Content`.
3. Return `202 Accepted` immediately; the worker runs in a goroutine so GitHub's 10-second webhook timeout is never hit.
4. The worker:
   - Posts an `eyes` (👀) reaction on the triggering comment.
   - Dispatches on the parsed command (`review`, `rereview`, `apply`, `skip`).
   - For reviews: creates a Reviewer, optionally clones the PR branch, discovers skills, loads the comment thread as conversation context (with the trigger pinned last), runs the review, optionally judges, optionally commits & pushes agent changes, then posts the review.
   - For `apply`: clones the PR branch, looks up the referenced `[#N]` suggestions on the bot's prior reviews, rewrites the files, then commits and pushes.
   - For `skip`: posts an acknowledgement comment (no state change — suggestions are just informational on GitHub).
   - On any error, posts a `## Review failed` comment so the human who mentioned the bot knows something went wrong.

### Comment command grammar

Every inline suggestion posted by the bot is prefixed with a stable `[#N]` index — numbered globally across all of the bot's prior review comments on the PR. Humans (or automation) can then reply to the bot with one of these verbs:

| Command | Effect |
|---|---|
| `@<bot-login>` | Run a fresh review (no verb = default) |
| `@<bot-login> review` | Same as bare mention |
| `@<bot-login> rereview` (or `re-review`) | Re-run the review, typically after pushing fixes |
| `@<bot-login> apply N` | Apply suggestion `[#N]`: rewrite the targeted file, commit, push |
| `@<bot-login> apply 1, 4, 7` | Apply multiple suggestions in one commit |
| `@<bot-login> apply all` | Apply every suggestion the bot has made on the PR |
| `@<bot-login> skip N` (or `dismiss N`) | Acknowledge dismissal of `[#N]` — posts a reply, no code change |

Rules:

- Only the first line of the comment is parsed — anything below is ignored, so you can write `@bot apply 3\n\nthanks!` safely.
- Arguments are whitespace- or comma-separated; `apply 1 2 3`, `apply 1,2,3`, and `apply 1, 2, 3` are all equivalent.
- Indices that don't resolve (out of range, or pointing at a comment with no ```` ```suggestion ```` block) come back in the `Missing` section of the reply comment.
- `apply` requires `-clone` on the server — without a workdir there's nothing to commit to.
- The bot applies edits highest-line-first per file so earlier edits don't invalidate later line numbers.

### Flags

| Flag | Description | Default |
|---|---|---|
| `-addr` | Listen address | `:8080` |
| `-bot-login` | Required. Only comments mentioning `@<bot-login>` trigger a review | — |
| `-provider` | `claude` or `gemini` | `claude` |
| `-model` | Model override | `""` |
| `-clone` | Shallow-clone the PR branch for every review | `true` |
| `-apply` | Commit & push agent-made file changes | `false` |
| `-skills` | Fallback skills directory (when `-clone` is off) | `.agents/skills` |
| `-judge` | Enable the judge | `false` |
| `-judge-min-score` | Judge threshold | `0.5` |

### Setting up the GitHub webhook

1. In the repo (or org) settings → Webhooks → Add webhook.
2. Payload URL: `https://<your-host>/webhook`. Content type: `application/json`. Secret: same value as `GITHUB_WEBHOOK_SECRET`.
3. Events: **Issue comments** (and **Pings** for testing). That's it — PR comments are delivered as `issue_comment` events.
4. `GITHUB_TOKEN` needs `contents:write` (for clone + push with `-apply`), `pull_requests:write` (reviews, reply), and `issues:write` (fallback comments, reactions). A fine-grained PAT or App installation token both work.

## Cloud Run reconciler (production)

The reconciler is `serve`'s grown-up sibling — same review engine, but the webhook-to-work plumbing is replaced with a workqueue so bursts dedupe and failures retry. It's the right choice when you're running the bot across multiple repos or teams and can't afford to miss events.

### Shape

```
GitHub ─webhook─▶ github-events  ──▶ broker ──▶ workqueue ──▶ reconciler ──▶ GitHub
                 (Cloud Run)         (Pub/Sub)   (GCS+PubSub)  (cmd/reconciler)
```

- `github-events` (Chainguard's module) handles HMAC verification and emits a CloudEvent per GitHub webhook.
- The broker → workqueue bridge subscribes to `dev.chainguard.github.issue_comment` and uses the `pullrequesturl` CloudEvent extension as the workqueue **key**.
- The workqueue dedupes by key, retries on error with exponential backoff, and dead-letters after `max_retry` (default 5).
- Our code (`cmd/reconciler/main.go`) is a ~150-line gRPC `WorkqueueService.Process(key)` handler — it parses the PR URL, calls `RunReview`, and lets the workqueue manage retries.

### Deploying

One-line summary: edit a variable, `terraform apply`, populate two secrets, add a GitHub webhook. Full walkthrough lives in [`deploy/terraform/README.md`](deploy/terraform/README.md) — it covers enabling APIs, creating the bot token secret, running apply (~10 min first time, mostly `ko` building the image), populating the HMAC secret, wiring the webhook, and a smoke test.

For a guided deploy, ask Claude Code "deploy code-reviewer" to invoke the [`code-reviewer-deploy`](~/.claude/skills/code-reviewer-deploy/SKILL.md) skill, which walks through each step with troubleshooting for the common failures.

### What's intentionally out

- **DNS + managed certs.** Uses the raw `.run.app` URL. Upgrade to `serverless-gclb` + a domain when you need a stable webhook URL behind your own hostname.
- **GitHub App auth.** Uses a PAT via Secret Manager. A GitHub App would rotate keys and scope per-install; add that when the reviewer is multi-tenant.
- **`apply` / `skip` verbs.** The reconciler only runs reviews today. If you need apply/skip in the deployed service, run `reviewer serve` alongside (or in front of) the reconciler — it's the tracked follow-up.

## Skills

Skills are progressive, on-demand context snippets the agent loads only when a task calls for them. Exactly the same layout openreview uses:

```
.agents/
└── skills/
    ├── go-review-style/
    │   └── SKILL.md
    └── security-checklist/
        └── SKILL.md
```

Each `SKILL.md` starts with YAML frontmatter:

```markdown
---
name: go-review-style
description: House style for Go code reviews — idioms, error handling, tests.
---

(Body — shown to the agent when it calls load_skill("go-review-style"))
```

On startup, the reviewer scans (in order):

1. `<workdir>/.agents/skills/` when a workdir is active (clone or `-workdir`).
2. The `-skills` directory (default `.agents/skills` relative to the current dir).

Discovered skill names and descriptions are injected into the system prompt. The agent retrieves full bodies via the `load_skill` tool only when it decides a skill is relevant — keeping the always-on prompt small.

## Workdir, Tools, and `-apply`

The reviewer maps openreview's Vercel Sandbox to a local workdir. A workdir is either:

- An **existing checkout** (`-workdir=<path>`) — nothing is cloned or cleaned up.
- A **shallow clone** (`-clone`) — repo is cloned into a temp dir with basic-auth HTTPS using `GITHUB_TOKEN`, then removed on exit.

With a workdir active, the agent gets these additional tools:

| Tool | Effect |
|---|---|
| `bash` | Runs a shell command inside the workdir. stdout/stderr are truncated at ~10 KB before returning. |
| `write_file` | Writes file contents. Paths resolved against the workdir root; escapes (`../`) are rejected. |
| `read_file` | Same as base mode but reads from the workdir first, falling back to the GitHub API. |

`-apply` closes the loop: after the review runs, any uncommitted edits the agent made via `write_file`/`bash` are committed (`code-reviewer: apply changes`) and pushed to the PR branch. `-apply` without a workdir is rejected at startup.

Token safety: commits are pushed via an `http.extraheader` injected into the clone's git config; the token never touches the working tree or the remote URL in `.git/config`.

## AI Judge

The judge evaluates each suggestion on:

- **Accuracy** — is the issue real?
- **Actionability** — is it specific enough to act on?
- **Value** — does it matter?
- **Clarity** — is the message clear?

Suggestions below `-judge-min-score` are dropped before posting. Enabled with `-judge`; tuned with `-judge-min-score` (and, in `review` mode, `-judge-model`).

```bash
reviewer review -owner=myorg -repo=myrepo -pr=123 -judge -judge-min-score=0.7
```

## Feature Parity with openreview

| Capability | openreview (TS / Vercel) | code-reviewer (Go) |
|---|---|---|
| Trigger | GitHub webhook (Next.js) | `pkg/webhook` HMAC verify + `/webhook` handler |
| Async work | Vercel Workflow | Goroutine + `202 Accepted` |
| Full-repo access | Vercel Sandbox | `pkg/workdir` (clone or reuse checkout) |
| Agent framework | Vercel AI SDK | Driftless `claudeexecutor` / `googleexecutor` |
| Models | Claude Sonnet 4.6, Gemini 2.5 Flash | Same, via Vertex AI |
| `read_file` tool | ✓ | ✓ |
| `bash` tool | ✓ | ✓ (workdir only) |
| `write_file` tool | ✓ | ✓ (workdir only) |
| `reply` tool | ✓ | ✓ (serve mode) |
| `load_skill` tool | ✓ | ✓ |
| Progressive skills (`.agents/skills/*/SKILL.md`) | ✓ | ✓ |
| @mention trigger | ✓ | ✓ (`-bot-login`) |
| Eyes-reaction ack | ✓ | ✓ |
| Commit/push agent edits | ✓ | ✓ (`-apply`) |
| Conversation context from prior PR comments | ✓ | ✓ |
| Reactions as approval signal (👍/👎) | ✓ | ✗ (GitHub webhooks don't emit reaction events natively) |
| Apply / skip via comment verbs (`@bot apply N`, `@bot skip N`, `@bot apply all`) | — | ✓ (stand-in for reactions) |
| GitHub App JWT auth | ✓ | ✗ (uses PAT / installation token via `GITHUB_TOKEN`) |

## Architecture

```
code-reviewer/
├── cmd/reviewer/
│   ├── main.go          # subcommand dispatcher
│   ├── review.go        # one-shot CLI
│   └── serve.go         # webhook server
├── pkg/
│   ├── github/client.go # GitHub API wrapper (PRs, comments, reactions)
│   ├── webhook/         # HMAC verify + issue_comment event parsing
│   ├── workdir/         # Local checkout abstraction (clone/use, bash, write, commit+push)
│   ├── skills/          # SKILL.md discovery + frontmatter parsing
│   └── reviewer/
│       ├── types.go     # ReviewResult, CodeSuggestion, JudgeConfig
│       ├── prompt.go    # Prompt template (diff, files, skills, conversation)
│       ├── tools.go     # Tool definitions (bash, write_file, load_skill, reply)
│       ├── reviewer.go  # Review orchestration (Claude + Gemini)
│       └── judge.go     # Judge integration
└── README.md
```

### driftlessaf packages used

| Package | Usage |
|---|---|
| `agents/executor/claudeexecutor` | Claude conversation loop |
| `agents/executor/googleexecutor` | Gemini conversation loop |
| `agents/promptbuilder` | Safe template binding with XML/CDATA |
| `agents/submitresult` | Structured result submission |
| `agents/toolcall/claudetool` | Claude tool parameter extraction |
| `agents/toolcall/googletool` | Gemini tool parameter extraction |
| `agents/agenttrace` | Tool-call trace types |
| `agents/judge` | Suggestion quality evaluator |

The reconciler is layered on top: `cmd/reconciler/main.go` registers a gRPC `WorkqueueService` that calls into `pkg/reviewer/runner.go` (shared with `serve`), and the Terraform stack in `deploy/terraform/` wires `github-events` + broker + `regional-go-reconciler` + `cloudevents-workqueue` to it.

## Development

### Prerequisites

- Go 1.25.4+
- Google Cloud project with Vertex AI enabled
- A GitHub token with repo access for testing

`chainguard.dev/driftlessaf` is pulled in as a regular module dependency (currently pinned to v0.5.0) — no local checkout required.

### Building and testing

```bash
go build ./...
go test ./...
```

### Local webhook testing

`smee.io` or `ngrok` works well for pointing GitHub webhooks at a local `reviewer serve` instance:

```bash
# 1. Terminal A
reviewer serve -bot-login=localbot -addr=:8080

# 2. Terminal B — forward github webhook deliveries
smee -u https://smee.io/<token> --target http://localhost:8080/webhook
```

Set `GITHUB_WEBHOOK_SECRET` on both sides, or leave it empty on the reviewer for unsigned local testing.

## License

Apache-2.0
