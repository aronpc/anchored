package sync

import (
	"context"
	"sort"

	"github.com/jholhewres/anchored/pkg/config"
)

// ResolveProjectAcrossRemotes finds which configured remote knows the given
// git-origin remote_key(s), returning that remote, the server-side project ID,
// and the key that matched on it.
//
// This is what makes a freshly-configured second server work with zero extra
// setup: `remote configure --name company` adds an entry with no routing
// paths, so path/default resolution alone would keep sending a company repo
// to the personal server (which rejects the unknown key). The probe asks each
// configured remote "do you know this repository?" and routes to the one that
// does.
//
// Each remote is probed with every key in order (canonical first, then the
// legacy key) so a project still registered under the old normalization on a
// not-yet-rekeyed server is found.
//
// Probe order is deterministic: the path/default-resolved remote for cwd
// first (so explicit routing always wins when its server knows the repo),
// then the remaining remotes by name. Remotes that fail to answer are
// skipped — best-effort. Returns (nil, "", "") when no remote knows any key.
func ResolveProjectAcrossRemotes(ctx context.Context, cfg *config.Config, cwd, clientID string, keys ...string) (*config.RemoteEntry, string, string) {
	if cfg == nil {
		return nil, "", ""
	}
	hasKey := false
	for _, k := range keys {
		if k != "" {
			hasKey = true
			break
		}
	}
	if !hasKey {
		return nil, "", ""
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
		if pid, matched := client.ResolveProjectIDByRemoteKeys(ctx, keys...); pid != "" {
			return e, pid, matched
		}
	}
	return nil, "", ""
}
