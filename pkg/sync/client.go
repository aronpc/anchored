package sync

import (
	"bytes"
	"context"
	"encoding/json"
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

// Push sends classified memories to the remote server.
// All memories are validated through RemoteSafetyFilter before sending.
// Any memory that would be blocked is rejected locally with a clear error.
// The caller MUST run ClassifyForPreview first and only push syncable memories.
func (c *Client) Push(ctx context.Context, req SyncPushRequest) (*SyncPushResponse, error) {
	// Validate all memories through RemoteSafetyFilter before sending.
	filtered := make([]SyncMemory, 0, len(req.Memories))
	var rejections []string
	for i := range req.Memories {
		m := &req.Memories[i]
		result := RemoteSafetyFilter(m.Content, nil, "")
		if result.Blocked {
			rejections = append(rejections, fmt.Sprintf("memory %s blocked: %s", m.ID, violationReason(result.Violations)))
			continue
		}
		filtered = append(filtered, *m)
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
		return nil, fmt.Errorf("push failed (%d): %s", resp.StatusCode, string(respBody))
	}

	var pushResp SyncPushResponse
	if err := json.NewDecoder(resp.Body).Decode(&pushResp); err != nil {
		return nil, fmt.Errorf("decode push response: %w", err)
	}

	// Merge local rejections into server response.
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
		return nil, fmt.Errorf("pull failed (%d): %s", resp.StatusCode, string(respBody))
	}

	var pullResp SyncPullResponse
	if err := json.NewDecoder(resp.Body).Decode(&pullResp); err != nil {
		return nil, fmt.Errorf("decode pull response: %w", err)
	}

	return &pullResp, nil
}

// Preview is a local-only operation that classifies memories for sync.
// It does NOT make any network request.
func (c *Client) Preview(memories []Memory, projectRoot string) PreviewResult {
	return ClassifyForPreview(memories, projectRoot)
}

func (c *Client) doRequest(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	url := c.serverURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	return c.httpClient.Do(req)
}
