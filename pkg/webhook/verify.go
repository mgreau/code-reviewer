/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

// Package webhook verifies and dispatches GitHub webhook events so the
// reviewer can act as an on-demand @mention bot, mirroring openreview's
// webhook-triggered workflow.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
)

// ErrInvalidSignature indicates the X-Hub-Signature-256 header did not match
// the body given the configured secret.
var ErrInvalidSignature = errors.New("invalid webhook signature")

// Verify reads r's body and validates it against the sha256 hmac in the
// X-Hub-Signature-256 header using secret. Returns the raw body on success so
// the caller can parse the event without re-reading the body. If secret is
// empty, the body is returned without verification (intended for local dev).
func Verify(r *http.Request, secret string) ([]byte, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	if secret == "" {
		return body, nil
	}

	sig := r.Header.Get("X-Hub-Signature-256")
	if !strings.HasPrefix(sig, "sha256=") {
		return nil, ErrInvalidSignature
	}
	want, err := hex.DecodeString(strings.TrimPrefix(sig, "sha256="))
	if err != nil {
		return nil, ErrInvalidSignature
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	got := mac.Sum(nil)
	if !hmac.Equal(got, want) {
		return nil, ErrInvalidSignature
	}
	return body, nil
}

// Event returns the X-GitHub-Event header.
func Event(r *http.Request) string {
	return r.Header.Get("X-GitHub-Event")
}
