/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package webhook

import (
	"encoding/json"
	"strconv"
	"strings"
)

// IssueCommentEvent is the trimmed subset of GitHub's issue_comment webhook
// payload that we actually need. Full payload here:
// https://docs.github.com/en/webhooks/webhook-events-and-payloads#issue_comment
type IssueCommentEvent struct {
	Action  string `json:"action"`
	Comment struct {
		ID   int64  `json:"id"`
		Body string `json:"body"`
		User struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"user"`
	} `json:"comment"`
	Issue struct {
		Number      int `json:"number"`
		PullRequest *struct {
			URL string `json:"url"`
		} `json:"pull_request"`
	} `json:"issue"`
	Repository struct {
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
}

// ParseIssueComment decodes an issue_comment event payload.
func ParseIssueComment(body []byte) (*IssueCommentEvent, error) {
	var ev IssueCommentEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return nil, err
	}
	return &ev, nil
}

// IsPRComment reports whether the comment was posted on a pull request (as
// opposed to a plain issue). GitHub sends the same event for both.
func (e *IssueCommentEvent) IsPRComment() bool {
	return e.Issue.PullRequest != nil
}

// MentionsBot reports whether the comment body contains an @mention for the
// given bot login. The check is case-insensitive and matches "@<login>"
// surrounded by whitespace or at the end of the body.
func (e *IssueCommentEvent) MentionsBot(botLogin string) bool {
	if botLogin == "" {
		return false
	}
	needle := "@" + strings.ToLower(strings.TrimPrefix(botLogin, "@"))
	body := strings.ToLower(e.Comment.Body)
	i := strings.Index(body, needle)
	if i < 0 {
		return false
	}
	// Ensure the match is a whole word (no prefix letter, no suffix letter).
	if i > 0 {
		prev := body[i-1]
		if isWordChar(prev) {
			return false
		}
	}
	end := i + len(needle)
	if end < len(body) {
		next := body[end]
		if isWordChar(next) {
			return false
		}
	}
	return true
}

// CommandType enumerates the verbs the bot understands in an @mention.
type CommandType int

const (
	// CmdReview is the default: run a full review pass.
	CmdReview CommandType = iota
	// CmdRereview is an explicit re-review request; identical behaviour to CmdReview.
	CmdRereview
	// CmdApply applies suggestion(s) from the most recent bot review.
	CmdApply
	// CmdSkip acknowledges and dismisses suggestion(s) without applying.
	CmdSkip
)

// Command is a parsed @mention verb plus arguments.
type Command struct {
	Type CommandType
	// All is true when the user wrote "apply all" / "skip all".
	All bool
	// Nums is the list of suggestion indices the user named (1-based).
	Nums []int
}

// ParseCommand extracts a verb+args from the comment body. The bot must be
// mentioned; the verb, if any, is whatever token immediately follows the
// mention on the same line. Unknown verbs (or a lone mention) become CmdReview.
//
// Supported grammars:
//
//	@bot                    -> CmdReview
//	@bot review             -> CmdReview
//	@bot rereview           -> CmdRereview
//	@bot apply all          -> CmdApply{All: true}
//	@bot apply 3            -> CmdApply{Nums: [3]}
//	@bot apply 1 2 5        -> CmdApply{Nums: [1,2,5]}
//	@bot apply 1,2,5        -> CmdApply{Nums: [1,2,5]}
//	@bot skip 4             -> CmdSkip{Nums: [4]}
//
// Returns ok=false when the bot isn't mentioned at all.
func (e *IssueCommentEvent) ParseCommand(botLogin string) (Command, bool) {
	if botLogin == "" {
		return Command{}, false
	}
	needle := "@" + strings.ToLower(strings.TrimPrefix(botLogin, "@"))
	body := e.Comment.Body
	lower := strings.ToLower(body)

	i := strings.Index(lower, needle)
	if i < 0 {
		return Command{}, false
	}
	// Verify word boundary so "@bottom" doesn't match "@bot".
	if i > 0 && isWordChar(lower[i-1]) {
		return Command{}, false
	}
	end := i + len(needle)
	if end < len(lower) && isWordChar(lower[end]) {
		return Command{}, false
	}

	// Take the rest of the line after the mention.
	rest := body[end:]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[:nl]
	}
	// Tokenize on whitespace and commas so "apply 1,2" works the same as "apply 1 2".
	fields := strings.FieldsFunc(rest, func(r rune) bool {
		return r == ' ' || r == '\t' || r == ',' || r == ';'
	})
	if len(fields) == 0 {
		return Command{Type: CmdReview}, true
	}

	verb := strings.ToLower(fields[0])
	args := fields[1:]

	switch verb {
	case "review":
		return Command{Type: CmdReview}, true
	case "rereview", "re-review":
		return Command{Type: CmdRereview}, true
	case "apply":
		return parseIndexedCommand(CmdApply, args), true
	case "skip", "dismiss":
		return parseIndexedCommand(CmdSkip, args), true
	default:
		// Unknown verb — treat as plain review so freeform mentions still work.
		return Command{Type: CmdReview}, true
	}
}

// parseIndexedCommand parses arguments that are either "all" or a list of
// positive integers.
func parseIndexedCommand(t CommandType, args []string) Command {
	cmd := Command{Type: t}
	for _, a := range args {
		low := strings.ToLower(a)
		if low == "all" {
			cmd.All = true
			cmd.Nums = nil
			return cmd
		}
		n, err := strconv.Atoi(a)
		if err != nil || n <= 0 {
			continue
		}
		cmd.Nums = append(cmd.Nums, n)
	}
	return cmd
}

func isWordChar(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z':
		return true
	case b >= 'A' && b <= 'Z':
		return true
	case b >= '0' && b <= '9':
		return true
	case b == '_' || b == '-':
		return true
	}
	return false
}
