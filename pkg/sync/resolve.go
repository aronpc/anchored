package sync

import (
	"context"
	"sort"

	"github.com/jholhewres/anchored/pkg/config"
)

// ResolveProjectAcrossRemotes finds which configured remote knows the given
// git-origin remote_key, returning that remote and the server-side project ID.
//
// This is what makes a freshly-configured second server work with zero extra
// setup: `remote configure --name company` adds an entry with no routing
// paths, so path/default resolution alone would keep sending a company repo
// to the personal server (which rejects the unknown key). The probe asks each
// configured remote "do you know this repository?" and routes to the one that
// does.
//
// Probe order is deterministic: the path/default-resolved remote for cwd
// first (so explicit routing always wins when its server knows the repo),
// then the remaining remotes by name. Remotes that fail to answer are
// skipped — best-effort. Returns (nil, "") when no remote knows the key.
func ResolveProjectAcrossRemotes(ctx context.Context, cfg *config.Config, cwd, remoteKey, clientID string) (*config.RemoteEntry, string) {
	if remoteKey == "" || cfg == nil {
		return nil, ""
	}

	ordered := make([]*config.RemoteEntry, 0, len(cfg.Remotes)+1)
	seen := map[string]bool{}
	if e := cfg.ResolveRemote(cwd); e != nil {
		ordered = append(ordered, e)
		seen[e.Name] = true
	}
	names := make([]string, 0, len(cfg.Remotes))
	for name := range cfg.Remotes {
		if !seen[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		e := cfg.Remotes[name]
		e.Name = name
		ordered = append(ordered, &e)
	}

	for _, e := range ordered {
		client := NewClientFromEntry(*e, clientID)
		if pid := client.ResolveProjectIDByRemoteKey(ctx, remoteKey); pid != "" {
			return e, pid
		}
	}
	return nil, ""
}
