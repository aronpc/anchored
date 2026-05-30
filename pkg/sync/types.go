package sync

// SyncPushRequest is the payload sent to anchored_oss for pushing local memories.
// ProjectRoot is a client-side hint used by the local safety filter to rewrite
// in-project paths to relative form. It is intentionally not serialized.
type SyncPushRequest struct {
	ClientID  string `json:"client_id"`
	ProjectID string `json:"project_id"`
	// ProjectClaim lets the client route a push to a remote project by its
	// git-origin remote_key instead of a server-side project_id. When set and
	// ProjectID is empty, the server resolves-or-creates the project by
	// remote_key, using Name for a human-readable label. This is how
	// repo-scoped sync identifies a repository regardless of its local
	// directory name.
	ProjectClaim *ProjectClaim `json:"project_claim,omitempty"`
	Memories     []SyncMemory  `json:"memories"`
	ProjectRoot  string        `json:"-"`
}

// SyncMemory is a memory ready for remote sync (no embeddings, no local paths).
type SyncMemory struct {
	ID               string `json:"id"`
	Category         string `json:"category"`
	Content          string `json:"content"`
	Source           string `json:"source"`
	PreferenceScope  string `json:"preference_scope,omitempty"`
	RemoteProjectKey string `json:"remote_project_key,omitempty"`
	Metadata         any    `json:"metadata,omitempty"`
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

type RemoteMemory struct {
	ID           string        `json:"id"`
	Category     string        `json:"category"`
	Content      string        `json:"content"`
	Source       string        `json:"source,omitempty"`
	ProjectID    string        `json:"project_id,omitempty"`
	ProjectClaim *ProjectClaim `json:"project_claim,omitempty"`
}

type ProjectClaim struct {
	Name      string `json:"name"`
	RemoteKey string `json:"remote_key"`
}

type SaveRemoteResponse struct {
	ID        string `json:"id"`
	Category  string `json:"category"`
	ProjectID string `json:"project_id,omitempty"`
	Created   bool   `json:"created"`
}

// SyncTripleRequest pushes a batch of knowledge-graph triples to the remote
// server. Triples are scoped to a project; the caller must have resolved the
// remote project ID (e.g. via a prior memory sync) before calling.
type SyncTripleRequest struct {
	Triples []SyncTriple `json:"triples"`
}

type SyncTriple struct {
	Subject    string  `json:"subject"`
	Predicate  string  `json:"predicate"`
	Object     string  `json:"object"`
	Confidence float64 `json:"confidence,omitempty"`
}

type SyncTripleResponse struct {
	Accepted int      `json:"accepted"`
	Rejected int      `json:"rejected"`
	Errors   []string `json:"errors,omitempty"`
}

type RemoteSearchResult struct {
	ID         string `json:"id"`
	Category   string `json:"category"`
	Content    string `json:"content"`
	ProjectID  string `json:"project_id"`
	Source     string `json:"source,omitempty"`
	AuthorName string `json:"author_name,omitempty"`
	UpdatedAt  string `json:"updated_at"`
}
