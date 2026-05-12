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
	"strconv"
	"strings"
	"time"
)

// DefaultEndpoint is the production Ramp API base URL.
const DefaultEndpoint = "https://api.ramp.com"

// vendorsPath is the relative path for the paginated accounting vendors
// listing.
const vendorsPath = "/developer/v1/accounting/vendors"

// defaultPageSize is the request page size when listing vendors. Ramp
// caps this at 100; we keep it at the maximum so the controller minimises
// round-trips.
const defaultPageSize = 100

// requestTimeout bounds every HTTP request the adapter makes. Token
// fetches, vendor list calls, and follow-up pages each get their own
// timeout via the parent context.
const requestTimeout = 30 * time.Second

// Config configures a Ramp client. Endpoint and TokenURL are optional and
// fall back to the production URLs.
type Config struct {
	Endpoint     string
	TokenURL     string
	ClientID     string
	ClientSecret string
	HTTPClient   *http.Client
}

// Client lists Ramp accounting vendors. Construct one per reconcile when
// the credentials Secret or endpoint may have changed; the OAuth2 token
// cache is owned by the Client so it lives only as long as the reconcile
// loop needs it.
type Client struct {
	endpoint   string
	httpClient *http.Client
	tokens     *tokenSource
}

// NewClient builds a Ramp client. Required fields are ClientID and
// ClientSecret; everything else falls back to a sensible default.
func NewClient(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.ClientID) == "" {
		return nil, errors.New("ramp: client_id is required")
	}
	if strings.TrimSpace(cfg.ClientSecret) == "" {
		return nil, errors.New("ramp: client_secret is required")
	}

	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	tokenURL := cfg.TokenURL
	if tokenURL == "" {
		tokenURL = DefaultTokenURL
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: requestTimeout}
	}

	return &Client{
		endpoint:   strings.TrimRight(endpoint, "/"),
		httpClient: httpClient,
		tokens:     newTokenSource(httpClient, tokenURL, cfg.ClientID, cfg.ClientSecret),
	}, nil
}

// ListActiveVendors walks every page of the accounting vendors endpoint
// and returns the union, filtered to vendors Ramp marks active. The
// controller is the only intended caller; if the listing grows past
// reasonable bounds (>5000 vendors) we should switch to a streamed
// callback API.
func (c *Client) ListActiveVendors(ctx context.Context) ([]AccountingVendor, error) {
	out := make([]AccountingVendor, 0, defaultPageSize)
	cursor := ""
	for page := 0; ; page++ {
		batch, next, err := c.listOnce(ctx, cursor)
		if err != nil {
			return nil, fmt.Errorf("list page %d: %w", page, err)
		}
		for _, v := range batch {
			if v.IsActive {
				out = append(out, v)
			}
		}
		if next == "" {
			return out, nil
		}
		cursor = next
		if page >= 200 {
			// Hard cap so a runaway pagination doesn't take the controller
			// out. 200 pages * 100 per page = 20k vendors; we're nowhere
			// near that today.
			return nil, fmt.Errorf("ramp: pagination did not terminate after %d pages", page)
		}
	}
}

func (c *Client) listOnce(ctx context.Context, cursor string) ([]AccountingVendor, string, error) {
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return nil, "", err
	}

	u, err := url.Parse(c.endpoint + vendorsPath)
	if err != nil {
		return nil, "", fmt.Errorf("parse endpoint: %w", err)
	}
	q := u.Query()
	q.Set("page_size", strconv.Itoa(defaultPageSize))
	if cursor != "" {
		q.Set("start", cursor)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", fmt.Errorf("build list request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("list request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, "", fmt.Errorf("ramp: rate limited (HTTP 429)")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf(
			"ramp list returned %d: %s",
			resp.StatusCode,
			strings.TrimSpace(string(body)),
		)
	}

	var payload ListAccountingVendorsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, "", fmt.Errorf("decode ramp list response: %w", err)
	}
	return payload.Data, payload.Page.Next, nil
}
