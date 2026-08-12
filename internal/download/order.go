package download

// DownloadOrder is the single shared source of truth for the configured
// download source priority order. All consumers (Orchestrator, monitoring
// service, download service) hold the same instance, backed by a live getter
// into the config, so runtime changes to download_order apply everywhere
// without a restart and without per-consumer wiring.
type DownloadOrder struct {
	get func() []string
}

// NewDownloadOrder creates a DownloadOrder backed by a live config getter.
func NewDownloadOrder(get func() []string) *DownloadOrder {
	return &DownloadOrder{get: get}
}

// Current returns the current priority order. A nil receiver or unset getter
// yields nil, which callers treat as "registration order".
func (o *DownloadOrder) Current() []string {
	if o == nil || o.get == nil {
		return nil
	}
	return o.get()
}
