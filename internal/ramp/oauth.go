// SPDX-License-Identifier: AGPL-3.0-only

package ramp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultTokenURL is the production Ramp OAuth2 token endpoint. Callers can
// override this through RampImportSource.TokenURL for sandbox setups.
const DefaultTokenURL = "https://api.ramp.com/developer/v1/token"

// requiredScope is the OAuth2 scope the importer requests. Ramp uses
// space-separated scopes; "accounting:read" gives access to the
// `/developer/v1/accounting/vendors` endpoint we list.
const requiredScope = "accounting:read"

// tokenSource hands out cached client_credentials access tokens. It is
// safe for concurrent use by a single controller (we never share a
// tokenSource between controllers, but the reconciler may be invoked
// concurrently for different VendorImport CRs in a future world).
type tokenSource struct {
	httpClient   *http.Client
	tokenURL     string
	clientID     string
	clientSecret string

	mu       sync.Mutex
	token    string
	expires  time.Time
	skewSafe time.Duration // refresh this much before actual expiry
}

func newTokenSource(httpClient *http.Client, tokenURL, clientID, clientSecret string) *tokenSource {
	return &tokenSource{
		httpClient:   httpClient,
		tokenURL:     tokenURL,
		clientID:     clientID,
		clientSecret: clientSecret,
		skewSafe:     30 * time.Second,
	}
}

// Token returns a valid access token, fetching a fresh one when the cached
// token is missing or close to expiry.
func (t *tokenSource) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.token != "" && time.Now().Add(t.skewSafe).Before(t.expires) {
		return t.token, nil
	}

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("scope", requiredScope)

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		t.tokenURL,
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return "", fmt.Errorf("build token request: %w", err)
	}
	req.SetBasicAuth(t.clientID, t.clientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch ramp token: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf(
			"ramp token endpoint returned %d: %s",
			resp.StatusCode,
			strings.TrimSpace(string(body)),
		)
	}

	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		TokenType   string `json:"token_type"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("decode ramp token response: %w", err)
	}
	if payload.AccessToken == "" {
		return "", errors.New("ramp token response missing access_token")
	}

	t.token = payload.AccessToken
	// Default to one hour if Ramp omits expires_in.
	expiresIn := time.Duration(payload.ExpiresIn) * time.Second
	if expiresIn <= 0 {
		expiresIn = time.Hour
	}
	t.expires = time.Now().Add(expiresIn)
	return t.token, nil
}
