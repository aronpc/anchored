package sync

func ClassifyForPreview(memories []Memory, projectRoot string) PreviewResult {
	result := PreviewResult{
		Total: len(memories),
	}

	for i := range memories {
		m := &memories[i]
		item := PreviewItem{Memory: *m}

		meta := toMap(m.Metadata)
		filterResult := RemoteSafetyFilter(m.Content, meta, projectRoot)
		// Propagate the safe (rewritten) content so downstream callers
		// that push item.Memory don't re-leak the original local paths.
		item.Memory.Content = filterResult.Content

		scope := m.PreferenceScope
		if scope == "" {
			scope = preferenceScope(meta)
		}

		switch {
		case filterResult.Blocked:
			item.Classification = ClassificationBlocked
			item.Reason = violationReason(filterResult.Violations)
		case m.SyncOrigin != "" && m.SyncOrigin != "local":
			item.Classification = ClassificationNeedsReview
			item.Reason = "already_synced"
		case m.SyncDirty:
			item.Classification = ClassificationNeedsReview
			item.Reason = "pending_changes"
		case scope == "user":
			item.Classification = ClassificationBlocked
			item.Reason = "personal_preference"
		default:
			item.Classification = ClassificationSyncable
			item.Reason = ""
		}

		switch item.Classification {
		case ClassificationSyncable:
			result.Syncable++
		case ClassificationBlocked:
			result.Blocked++
		case ClassificationNeedsReview:
			result.NeedsReview++
		}

		result.Items = append(result.Items, item)
	}

	return result
}

func toMap(v any) map[string]any {
	if v == nil {
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func preferenceScope(meta map[string]any) string {
	if meta == nil {
		return ""
	}
	s, ok := meta["scope"].(string)
	if !ok {
		return ""
	}
	return s
}

func violationReason(violations []RemoteSafetyViolation) string {
	if len(violations) == 0 {
		return "unknown"
	}
	return violations[0].Reason
}
