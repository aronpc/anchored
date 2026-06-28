package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// RemoteTaskThread is the wire shape of one personal-kanban card. Projects
// carry NAMES, not local IDs — local project IDs are meaningless to the
// server, and the kanban only needs human-readable labels.
type RemoteTaskThread struct {
	TaskKey     string         `json:"task_key"`
	ExternalRef string         `json:"external_ref,omitempty"`
	Status      string         `json:"status"`
	Projects    []string       `json:"projects,omitempty"`
	Journal     []string       `json:"journal,omitempty"`
	Details     map[string]any `json:"details,omitempty"`
}

// PushTaskThreads upserts the caller's task threads on the remote (PUT
// /v1/me/task-threads). The server derives the account from the API key —
// threads are private to it by construction.
func (c *Client) PushTaskThreads(ctx context.Context, threads []RemoteTaskThread) (int, error) {
	if len(threads) == 0 {
		return 0, nil
	}
	body, err := json.Marshal(map[string]any{"threads": threads})
	if err != nil {
		return 0, fmt.Errorf("marshal task threads: %w", err)
	}
	resp, err := c.doRequest(ctx, http.MethodPut, "/v1/me/task-threads", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return 0, fmt.Errorf("push task threads: HTTP %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		Saved int `json:"saved"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("decode response: %w", err)
	}
	return out.Saved, nil
}
