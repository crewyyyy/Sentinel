package rules

// MergeOptions overlays explicit settings without sharing caller-owned maps or slices.
// Missing fields continue to inherit YAML/catalog defaults.
func MergeOptions(base, override Options) Options {
	out := copyOptions(base)
	for family, cap := range override.FamilyCaps {
		out.FamilyCaps[family] = cap
	}
	for id, module := range copyOptions(override).Modules {
		current := out.Modules[id]
		if module.Enabled != nil {
			current.Enabled = module.Enabled
		}
		if current.Matchers == nil {
			current.Matchers = map[string]MatcherConfig{}
		}
		if current.Thresholds == nil {
			current.Thresholds = map[string]int{}
		}
		for key, matcher := range module.Matchers {
			inherited := current.Matchers[key]
			if matcher.Mode != "" {
				inherited.Mode = matcher.Mode
			}
			if matcher.Terms != nil {
				inherited.Terms = matcher.Terms
			}
			current.Matchers[key] = inherited
		}
		for key, value := range module.Thresholds {
			current.Thresholds[key] = value
		}
		out.Modules[id] = current
	}
	return out
}

func (a *Analyzer) Resolved() ResolvedSnapshot {
	return a.Snapshot().(ResolvedSnapshot)
}
