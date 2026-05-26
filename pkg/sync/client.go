package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jholhewres/anchored/pkg/config"
)

// Client is a minimal HTTP client for the anchored_oss sync server.
// It is deliberately simple: no retries, no background goroutines, no auto-sync.
// All operations are explicit and require a server URL.
type Client struct {
	httpClient *http.Client
	serverURL  string
	apiKey     string
	clientID   string
}

// NewClient creates a sync client from RemoteConfig.
// Returns nil when RemoteConfig.Enabled is false.
func NewClient(cfg config.RemoteConfig, clientID string) *Client {
	if !cfg.Enabled {
		return nil
	}
	return &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		serverURL:  cfg.ServerURL,
		apiKey:     cfg.APIKey,
		clientID:   clientID,
	}
}

// HTTPError represents a non-2xx response from the sync server.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("sync server returned %d: %s", e.Status, e.Body)
}

type RemoteError struct {
	StatusCode int
	Body       string
}

func (e *RemoteError) Error() string {
	return fmt.Sprintf("remote server returned %d: %s", e.StatusCode, e.Body)
}

func IsRemoteForbidden(err error) bool {
	var re *RemoteError
	return errors.As(err, &re) && re.StatusCode == http.StatusForbidden
}

func IsRemoteUnavailable(err error) bool {
	var re *RemoteError
	return errors.As(err, &re) && re.StatusCode >= http.StatusInternalServerError
}

func NewClientFromEntry(entry config.RemoteEntry, clientID string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		serverURL:  entry.ServerURL,
		apiKey:     entry.APIKey,
		clientID:   clientID,
	}
}

// Push sends classified memories to the remote server.
// All memories are validated through RemoteSafetyFilter before sending using
// the same projectRoot the caller used for preview, so a memory classified as
// syncable cannot be silently re-blocked here. Personal preferences
// (PreferenceScope=="user") are rejected as a defense-in-depth net even if
// the caller forgot to pre-filter them.
//
// The caller SHOULD run ClassifyForPreview first and only push syncable
// memories; this method is the last line of defense.
func (c *Client) Push(ctx context.Context, req SyncPushRequest) (*SyncPushResponse, error) {
	filtered := make([]SyncMemory, 0, len(req.Memories))
	var rejections []string
	for i := range req.Memories {
		m := req.Memories[i]
		if m.Category == "event" || m.Category == "preference" {
			rejections = append(rejections, fmt.Sprintf("memory %s blocked: category_remote_blocked", m.ID))
			continue
		}

		if m.PreferenceScope == "user" {
			rejections = append(rejections, fmt.Sprintf("memory %s blocked: personal_preference", m.ID))
			continue
		}

		result := RemoteSafetyFilter(m.Content, nil, req.ProjectRoot)
		if result.Blocked {
			rejections = append(rejections, fmt.Sprintf("memory %s blocked: %s", m.ID, violationReason(result.Violations)))
			continue
		}
		// Always send the rewritten (safe) content, never the raw one.
		m.Content = result.Content
		filtered = append(filtered, m)
	}

	// Short-circuit when nothing survives the local filter — no point spending
	// a network round-trip and an auth token to push zero memories.
	if len(filtered) == 0 {
		return &SyncPushResponse{
			Accepted: 0,
			Rejected: len(rejections),
			Errors:   rejections,
		}, nil
	}

	req.Memories = filtered

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal push request: %w", err)
	}

	resp, err := c.doRequest(ctx, http.MethodPost, "/api/v1/sync/push", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("push request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("push failed: %w", &HTTPError{Status: resp.StatusCode, Body: string(respBody)})
	}

	var pushResp SyncPushResponse
	if err := json.NewDecoder(resp.Body).Decode(&pushResp); err != nil {
		return nil, fmt.Errorf("decode push response: %w", err)
	}

	if len(rejections) > 0 {
		pushResp.Rejected += len(rejections)
		pushResp.Errors = append(pushResp.Errors, rejections...)
	}

	return &pushResp, nil
}

// Pull fetches new/updated memories from the remote server since the given watermark.
// The response memories are not filtered — the server is trusted to send safe content.
func (c *Client) Pull(ctx context.Context, req SyncPullRequest) (*SyncPullResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal pull request: %w", err)
	}

	resp, err := c.doRequest(ctx, http.MethodPost, "/api/v1/sync/pull", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("pull request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("pull failed: %w", &HTTPError{Status: resp.StatusCode, Body: string(respBody)})
	}

	var pullResp SyncPullResponse
	if err := json.NewDecoder(resp.Body).Decode(&pullResp); err != nil {
		return nil, fmt.Errorf("decode pull response: %w", err)
	}

	return &pullResp, nil
}

func (c *Client) SaveRemote(ctx context.Context, mem RemoteMemory) (*SaveRemoteResponse, error) {
	filtered := []string{"event", "preference"}
	for _, blocked := range filtered {
		if mem.Category == blocked {
			return nil, fmt.Errorf("category %q blocked for remote save", mem.Category)
		}
	}

	result := RemoteSafetyFilter(mem.Content, nil, "")
	if result.Blocked {
		return nil, fmt.Errorf("content blocked by safety filter: %s", violationReason(result.Violations))
	}
	mem.Content = result.Content

	body, err := json.Marshal(mem)
	if err != nil {
		return nil, fmt.Errorf("marshal save request: %w", err)
	}

	resp, err := c.doRequest(ctx, http.MethodPost, "/v1/memories", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("save request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		return nil, &RemoteError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}
	if resp.StatusCode >= 400 {
		return nil, &RemoteError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	var saveResp SaveRemoteResponse
	if err := json.Unmarshal(respBody, &saveResp); err != nil {
		return nil, fmt.Errorf("decode save response: %w", err)
	}

	return &saveResp, nil
}

func (c *Client) SearchRemote(ctx context.Context, projectID string, query string, limit int) ([]RemoteSearchResult, error) {
	url := fmt.Sprintf("/v1/memories/search?project_id=%s&q=%s&limit=%d",
		urlQueryEscape(projectID),
		urlQueryEscape(query),
		limit,
	)

	resp, err := c.doRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("search request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		return nil, &RemoteError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}
	if resp.StatusCode >= 400 {
		return nil, &RemoteError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	var results []RemoteSearchResult
	if err := json.Unmarshal(respBody, &results); err != nil {
		return nil, fmt.Errorf("decode search response: %w", err)
	}

	return results, nil
}

// PushTriples sends a batch of knowledge-graph triples to the remote server
// for a previously-resolved project. The server applies the same hardening
// rules as anchored_oss does for memories (logical dedup, functional
// supersession, alias resolution).
//
// Caller is expected to have already resolved the remote projectID via a
// memory sync (the server's project_claim flow). PushTriples does not perform
// safety filtering — triples are entity strings, not free text, and the
// server's quality/policy filter doesn't apply to them.
func (c *Client) PushTriples(ctx context.Context, projectID string, triples []SyncTriple) (*SyncTripleResponse, error) {
	if projectID == "" {
		return nil, fmt.Errorf("PushTriples: projectID is required")
	}
	if len(triples) == 0 {
		return &SyncTripleResponse{}, nil
	}

	body, err := json.Marshal(SyncTripleRequest{Triples: triples})
	if err != nil {
		return nil, fmt.Errorf("marshal triples request: %w", err)
	}

	path := "/v1/projects/" + urlQueryEscape(projectID) + "/triples"
	resp, err := c.doRequest(ctx, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("push triples request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		return nil, &RemoteError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}
	if resp.StatusCode >= 400 {
		return nil, &RemoteError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	var out SyncTripleResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decode triples response: %w", err)
	}
	return &out, nil
}

func urlQueryEscape(s string) string {
	var buf bytes.Buffer
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isURLSafe(c) {
			buf.WriteByte(c)
		} else {
			fmt.Fprintf(&buf, "%%%02X", c)
		}
	}
	return buf.String()
}

func isURLSafe(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~'
}

func (c *Client) doRequest(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	url := c.serverURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.httpClient.Do(req)
}
