/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package webhook

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVerifyValid(t *testing.T) {
	secret := "shh"
	body := []byte(`{"action":"created"}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))

	got, err := Verify(req, secret)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Error("body mismatch")
	}
}

func TestVerifyInvalid(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	if _, err := Verify(req, "secret"); err == nil {
		t.Error("expected ErrInvalidSignature")
	}
}

func TestVerifyNoSecretBypasses(t *testing.T) {
	body := []byte(`{"action":"x"}`)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	got, err := Verify(req, "")
	if err != nil {
		t.Fatalf("Verify without secret: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Error("body mismatch")
	}
}
