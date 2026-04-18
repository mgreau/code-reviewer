/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package webhook

import "testing"

func TestMentionsBot(t *testing.T) {
	cases := []struct {
		name string
		body string
		bot  string
		want bool
	}{
		{"exact", "@bot please review", "bot", true},
		{"with at prefix", "@bot please review", "@bot", true},
		{"case insensitive", "@BOT please review", "bot", true},
		{"end of string", "hello @bot", "bot", true},
		{"no mention", "bot help", "bot", false},
		{"partial match", "@bottom please", "bot", false},
		{"empty login", "@bot", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev := &IssueCommentEvent{}
			ev.Comment.Body = c.body
			got := ev.MentionsBot(c.bot)
			if got != c.want {
				t.Errorf("MentionsBot(%q, %q) = %v, want %v", c.body, c.bot, got, c.want)
			}
		})
	}
}

func TestParseCommand(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		bot     string
		wantOK  bool
		wantCmd Command
	}{
		{"no bot login", "@bot review", "", false, Command{}},
		{"no mention", "hello world", "bot", false, Command{}},
		{"bare mention", "@bot", "bot", true, Command{Type: CmdReview}},
		{"explicit review", "@bot review", "bot", true, Command{Type: CmdReview}},
		{"rereview", "@bot rereview", "bot", true, Command{Type: CmdRereview}},
		{"re-review dashed", "@bot re-review", "bot", true, Command{Type: CmdRereview}},
		{"apply all", "@bot apply all please", "bot", true, Command{Type: CmdApply, All: true}},
		{"apply single", "@bot apply 3", "bot", true, Command{Type: CmdApply, Nums: []int{3}}},
		{"apply spaces", "@bot apply 1 2 5", "bot", true, Command{Type: CmdApply, Nums: []int{1, 2, 5}}},
		{"apply commas", "@bot apply 1,2,5", "bot", true, Command{Type: CmdApply, Nums: []int{1, 2, 5}}},
		{"apply mixed", "@bot apply 1, 2 ,3", "bot", true, Command{Type: CmdApply, Nums: []int{1, 2, 3}}},
		{"skip single", "@bot skip 4", "bot", true, Command{Type: CmdSkip, Nums: []int{4}}},
		{"dismiss alias", "@bot dismiss 2", "bot", true, Command{Type: CmdSkip, Nums: []int{2}}},
		{"unknown verb falls back to review", "@bot please take a look", "bot", true, Command{Type: CmdReview}},
		{"case insensitive bot", "@BOT APPLY ALL", "bot", true, Command{Type: CmdApply, All: true}},
		{"bot login with at prefix", "@bot apply 1", "@bot", true, Command{Type: CmdApply, Nums: []int{1}}},
		{"partial bot name no match", "@bottom apply 1", "bot", false, Command{}},
		{"negative number ignored", "@bot apply -1 2", "bot", true, Command{Type: CmdApply, Nums: []int{2}}},
		{"ignores text on subsequent lines", "@bot apply 2\n\nthanks!", "bot", true, Command{Type: CmdApply, Nums: []int{2}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev := &IssueCommentEvent{}
			ev.Comment.Body = c.body
			got, ok := ev.ParseCommand(c.bot)
			if ok != c.wantOK {
				t.Fatalf("ParseCommand(%q,%q) ok=%v want %v", c.body, c.bot, ok, c.wantOK)
			}
			if !ok {
				return
			}
			if got.Type != c.wantCmd.Type || got.All != c.wantCmd.All {
				t.Errorf("Command = %+v, want %+v", got, c.wantCmd)
			}
			if !intsEqual(got.Nums, c.wantCmd.Nums) {
				t.Errorf("Nums = %v, want %v", got.Nums, c.wantCmd.Nums)
			}
		})
	}
}

func intsEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestParseIssueComment(t *testing.T) {
	body := []byte(`{
		"action": "created",
		"comment": {"id": 42, "body": "@reviewer look", "user": {"login": "alice", "type": "User"}},
		"issue": {"number": 7, "pull_request": {"url": "https://..."}},
		"repository": {"name": "repo", "owner": {"login": "org"}}
	}`)
	ev, err := ParseIssueComment(body)
	if err != nil {
		t.Fatalf("ParseIssueComment: %v", err)
	}
	if ev.Issue.Number != 7 || !ev.IsPRComment() || ev.Repository.Owner.Login != "org" {
		t.Errorf("unexpected event: %+v", ev)
	}
}
