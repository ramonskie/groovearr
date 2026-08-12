package metadata

// ProviderOrder is the single shared source of truth for the configured
// metadata provider priority order. All consumers (MetadataResolver,
// MetadataEnrichmentHandler) hold the same instance, backed by a live getter
// into the config, so runtime changes to metadata_order apply everywhere
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
