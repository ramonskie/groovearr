package metadata

// ProviderOrder is the single shared source of truth for the configured
// metadata provider priority order. All consumers (MetadataResolver,
// MetadataEnrichmentHandler) hold the same instance, backed by a live getter
// into the config, so runtime changes to provider_order apply everywhere
// without a restart and without per-consumer wiring.
type ProviderOrder struct {
	get func() []string
}

// NewProviderOrder creates a ProviderOrder backed by a live config getter.
func NewProviderOrder(get func() []string) *ProviderOrder {
	return &ProviderOrder{get: get}
}

// Current returns the current priority order. A nil receiver or unset getter
// yields nil, which callers treat as "registration order".
func (o *ProviderOrder) Current() []string {
	if o == nil || o.get == nil {
		return nil
	}
	return o.get()
}

// Named is any value carrying a canonical provider name: a metadata.Provider, a
// discovery.Provider, or a plugin.BasePlugin. It is the only thing the order
// needs to key on, so the ordering helper stays provider-agnostic (AGENTS §1).
type Named interface {
	Name() string
}

// Order returns items sorted into the current provider priority order: items
// whose Name() appears in the order come first, in that order; every remaining
// item follows in its original relative order (stable). It is the single
// ordering rule every provider path shares (D-001 §3).
//
// A nil ProviderOrder, an unset getter, an empty order, or an empty items slice
// returns items unchanged — the "registration order" behavior callers had before
// the unification, so an install with no order configured keeps its current
// iteration order. It never filters, so a caller sees exactly the items it
// passed, only reordered.
func Order[T Named](o *ProviderOrder, items []T) []T {
	if len(items) == 0 || o == nil {
		return items
	}
	order := o.Current()
	if len(order) == 0 {
		return items
	}
	byName := make(map[string]T, len(items))
	for _, it := range items {
		if _, dup := byName[it.Name()]; !dup {
			byName[it.Name()] = it
		}
	}
	out := make([]T, 0, len(items))
	seen := make(map[string]bool, len(order))
	for _, name := range order {
		if seen[name] {
			continue
		}
		it, ok := byName[name]
		if !ok {
			continue
		}
		out = append(out, it)
		seen[name] = true
	}
	for _, it := range items {
		if !seen[it.Name()] {
			out = append(out, it)
		}
	}
	return out
}
