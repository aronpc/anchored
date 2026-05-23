package sync

// SyncPushRequest is the payload sent to anchored_oss for pushing local memories.
type SyncPushRequest struct {
	ClientID  string       `json:"client_id"`
	ProjectID string       `json:"project_id"`
	Memories  []SyncMemory `json:"memories"`
}

// SyncMemory is a memory ready for remote sync (no embeddings, no local paths).
type SyncMemory struct {
	ID               string `json:"id"`
	Category         string `json:"category"`
	Content          string `json:"content"`
	Source           string `json:"source"`
	PreferenceScope  string `json:"preference_scope,omitempty"`
	RemoteProjectKey string `json:"remote_project_key,omitempty"`
}

// SyncPushResponse is the server's response to a push request.
type SyncPushResponse struct {
	Accepted int      `json:"accepted"`
	Rejected int      `json:"rejected"`
	Errors   []string `json:"errors,omitempty"`
}

// SyncPullRequest requests new/updated memories from the server.
type SyncPullRequest struct {
	ClientID  string `json:"client_id"`
	ProjectID string `json:"project_id"`
	Watermark string `json:"watermark,omitempty"`
}

// SyncPullResponse is the server's response with remote memories.
type SyncPullResponse struct {
	Memories  []SyncMemory `json:"memories"`
	Watermark string       `json:"watermark"`
}
