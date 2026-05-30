package sync

import "github.com/jholhewres/anchored/pkg/memory"

// ClassifyForAutoSync decides whether a single memory may leave the machine for
// the auto write-through path. It runs the same classification as the
// preview/sync path (secrets, local paths, user scope, episodic/precompact, low
// quality, blocked categories). When syncable it returns the REDACTED content
// (home paths rewritten to <user-home>, etc.) — callers MUST push this, never
// the original, so redactions aren't bypassed. ok is false for anything not
// classified Syncable.
func ClassifyForAutoSync(m memory.Memory, projectRoot string) (content string, ok bool) {
	res := ClassifyForPreview([]Memory{ToSyncMemory(m)}, projectRoot)
	if len(res.Items) != 1 || res.Items[0].Classification != ClassificationSyncable {
		return "", false
	}
	return res.Items[0].Memory.Content, true
}

// ToSyncMemory converts a local memory into the sync wire model. Exported so
// both the CLI sync command and the MCP auto write-through path share one
// mapping.
func ToSyncMemory(m memory.Memory) Memory {
	return Memory{
		ID:               m.ID,
		Category:         m.Category,
		Content:          m.Content,
		ProjectID:        m.ProjectID,
		Source:           m.Source,
		SyncOrigin:       m.SyncOrigin,
		SyncDirty:        m.SyncDirty,
		RemoteProjectKey: m.RemoteProjectKey,
		PreferenceScope:  preferenceScopeFromMetadata(m.Metadata),
		Metadata:         m.Metadata,
	}
}

func preferenceScopeFromMetadata(v any) string {
	switch m := v.(type) {
	case memory.MemoryMetadata:
		return m.PreferenceScope
	case map[string]any:
		s, _ := m["scope"].(string)
		return s
	default:
		return memory.ParseMetadata(v).PreferenceScope
	}
}
