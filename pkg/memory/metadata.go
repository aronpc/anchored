package memory

import "encoding/json"

const (
	PreferenceScopeUser    = "user"
	PreferenceScopeProject = "project"
	PreferenceScopeTeam    = "team"
)

// MemoryMetadata provides structured metadata for memories.
// Stored as JSON in the Memory.Metadata field (which is `any` for backward compat).
type MemoryMetadata struct {
	Source        string   `json:"source,omitempty"`         // "user", "auto_capture", "dream", "import", "precompact"
	SessionID     string   `json:"session_id,omitempty"`     // Source session that created this memory
	Consolidated  []string `json:"consolidated,omitempty"`   // IDs of memories merged into this one
	DreamVersion  string   `json:"dream_version,omitempty"`  // Dream run ID that processed this
	CaptureReason string   `json:"capture_reason,omitempty"` // Why this was captured
	QualityScore  float64  `json:"quality_score,omitempty"`  // Auto-capture quality score
	// PreferenceScope distinguishes personal preferences from project/team conventions.
	// Valid values: "user", "project", "team". Empty means unknown/non-preference.
	PreferenceScope string `json:"preference_scope,omitempty"`
}

// ToAny converts MemoryMetadata to the `any` type expected by Memory.Metadata.
func (m MemoryMetadata) ToAny() any {
	// Return nil if all fields are zero
	if m.Source == "" && m.SessionID == "" && len(m.Consolidated) == 0 && m.DreamVersion == "" && m.CaptureReason == "" && m.QualityScore == 0 && m.PreferenceScope == "" {
		return nil
	}
	return m
}

func NormalizePreferenceScope(scope string) string {
	switch scope {
	case PreferenceScopeProject:
		return PreferenceScopeProject
	case PreferenceScopeTeam:
		return PreferenceScopeTeam
	default:
		return PreferenceScopeUser
	}
}

func WithPreferenceScope(metadata any, category, scope string) any {
	if category != "preference" {
		m := ParseMetadata(metadata)
		if m.PreferenceScope == "" {
			return metadata
		}
		m.PreferenceScope = ""
		return m.ToAny()
	}
	m := ParseMetadata(metadata)
	m.PreferenceScope = NormalizePreferenceScope(scope)
	return m.ToAny()
}

// MarshalJSON implements json.Marshaler using an alias to avoid infinite recursion.
func (m MemoryMetadata) MarshalJSON() ([]byte, error) {
	type alias MemoryMetadata
	return json.Marshal(alias(m))
}

// ParseMetadata parses an `any` (from Memory.Metadata) into MemoryMetadata.
// Returns zero-value MemoryMetadata if input is nil or unparseable.
func ParseMetadata(v any) MemoryMetadata {
	if v == nil {
		return MemoryMetadata{}
	}

	// If already MemoryMetadata, return as-is
	if m, ok := v.(MemoryMetadata); ok {
		return m
	}

	// If map, marshal and unmarshal
	b, err := json.Marshal(v)
	if err != nil {
		return MemoryMetadata{}
	}
	var m MemoryMetadata
	if err := json.Unmarshal(b, &m); err != nil {
		return MemoryMetadata{}
	}
	return m
}
