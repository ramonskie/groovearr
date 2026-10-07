package tracking

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/download"
	"github.com/ramonskie/groovearr/internal/library"
	"github.com/ramonskie/groovearr/internal/metadata"
)

// Exported sentinels let callers classify tracking failures with errors.Is
// (AGENTS §10). They are wrapped with %w so the provider-specific context is
// preserved while the API layer can map them to the right HTTP status.
var (
	// ErrProviderNotRegistered means no discovery provider is registered under
	// the requested name.
	ErrProviderNotRegistered = errors.New("tracking: discovery provider not registered")
	// ErrProviderCoolingDown means the provider is parked in the shared
	// rate-limit bucket (AGENTS §8) and was skipped without a provider call.
	ErrProviderCoolingDown = errors.New("tracking: provider cooling down")
	// ErrAlbumStatusInvalid means SetAlbumStatus was asked for a status other
	// than wanted or ignored; the API maps it to a 400.
	ErrAlbumStatusInvalid = errors.New("tracking: invalid album status")
	// ErrAlbumNotFound means the target tracked album does not exist; the API
	// maps it to a 404.
	ErrAlbumNotFound = errors.New("tracking: tracked album not found")
)

// RateLimiter is the shared per-provider cooldown bucket (AGENTS §8). It is
// satisfied by *metadata.ProviderCooldown. A nil RateLimiter means "nothing is
// cooling down", so the service behaves as if every provider is available.
type RateLimiter interface {
	CoolingDown(name string) bool
	MarkAfter(name string, retryAfter time.Duration)
}

// Compile-time guarantee that the canonical shared bucket can be injected.
var _ RateLimiter = (*metadata.ProviderCooldown)(nil)

// DownloadQueuer delegates album acquisition to the single canonical
// policy in internal/download — tracking must not re-implement it.
//
// requestedByUserID is the DB-only requester; tracking runs as a background
// job and always passes 0 (system/unknown).
type DownloadQueuer interface {
	QueueAlbumWithFallback(ctx context.Context, requestedByUserID int64, requestedByUsername, artist, album string, tracks []download.TrackQueue, downloadClient string, albumSources []string) (download.AlbumQueueResult, error)
}

// ActiveDownloadFinder exposes the download records SearchMissing needs to
// dedup and to re-arm exhausted retries. It is satisfied by *download.Service
// (List + Retry). A minimal interface is used because download.Store's
// FindActiveByTitle cannot see terminal failed records, and adding a method to
// download.Store/library.Store for tracking-only needs would leak this feature
// into their contracts (AGENTS §3).
type ActiveDownloadFinder interface {
	// List returns every download record, in any state.
	List(ctx context.Context) ([]download.Record, error)
	// Retry re-arms a failed download record in place.
	Retry(ctx context.Context, id string) error
}

// Compile-time guarantees that the canonical concretes satisfy the narrow
// tracking interfaces, so wiring in app.go stays type-safe.
var (
	_ DownloadQueuer       = (*download.Service)(nil)
	_ ActiveDownloadFinder = (*download.Service)(nil)
)

// requeueCooldown mirrors playlist.requeueCooldown: an exhausted-failed
// download must sit this long before a periodic SearchMissing re-arms it.
// Without the gate every refresh would reset the retry budget and a
// permanently unavailable release would hammer the providers forever.
var requeueCooldown = 24 * time.Hour

// artistAlbumsLimit caps how many releases are pulled per artist. Providers
// treat it as a soft maximum; a discography larger than this is completed on
// subsequent reconciles rather than blocking the current one.
const artistAlbumsLimit = 500

// Service reconciles tracked artists and their discographies between the
// discovery providers and the local library. It holds no provider-specific
// knowledge — every provider interaction goes through discovery.Registry.
type Service struct {
	store        Store
	discoveryReg *discovery.Registry
	libStore     library.Store
	rateLimit    RateLimiter
	queuer       DownloadQueuer
	activeFinder ActiveDownloadFinder
	cfgFn        func() config.Config
	log          *slog.Logger
}

// NewService creates a tracking service. rateLimit may be nil (nothing is
// treated as cooling down); queuer and activeFinder may be nil when only
// discography reconciliation is used (AddArtist/ReconcileAlbums). cfgFn
// returns live config and may be nil, in which case config.DefaultConfig
// is used.
func NewService(store Store, discoveryReg *discovery.Registry, libStore library.Store, rateLimit RateLimiter, queuer DownloadQueuer, activeFinder ActiveDownloadFinder, cfgFn func() config.Config, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	if cfgFn == nil {
		cfgFn = func() config.Config { return config.DefaultConfig() }
	}
	return &Service{
		store:        store,
		discoveryReg: discoveryReg,
		libStore:     libStore,
		rateLimit:    rateLimit,
		queuer:       queuer,
		activeFinder: activeFinder,
		cfgFn:        cfgFn,
		log:          logger,
	}
}

// RefreshResult summarizes one RefreshArtist pass.
type RefreshResult struct {
	AlbumsSeen  int `json:"albums_seen"`
	NewlyWanted int `json:"newly_wanted"`
	Missing     int `json:"missing"`
}

// SearchResult summarizes one SearchMissing pass.
type SearchResult struct {
	Queued int `json:"queued"`
	// Skipped counts albums already in the download pipeline plus albums the
	// canonical policy processed without queuing anything (Queued==0 and no
	// Errors); both still advance the rotating batch.
	Skipped int `json:"skipped"`
	Errors  int `json:"errors"`
	// Remaining is the number of wanted albums left unprocessed because the
	// per-run batch cap was reached. They stay wanted and are picked up by the
	// next run.
	Remaining int `json:"remaining"`
}

// fetchArtistAlbums pulls a discography through the discovery provider,
// honoring the shared cooldown before the call and parking the provider on
// metadata.ErrRateLimited exactly as the canonical path does.
func (s *Service) fetchArtistAlbums(ctx context.Context, provider discovery.Provider, providerName, providerArtistID string) ([]discovery.AlbumResult, error) {
	if s.coolingDown(providerName) {
		return nil, fmt.Errorf("%w: %q", ErrProviderCoolingDown, providerName)
	}
	albums, err := provider.GetArtistAlbums(ctx, providerArtistID, artistAlbumsLimit)
	if err != nil {
		s.noteRateLimit(providerName, err)
		return nil, fmt.Errorf("get artist albums from %q: %w", providerName, err)
	}
	return albums, nil
}

func (s *Service) coolingDown(name string) bool {
	return s.rateLimit != nil && s.rateLimit.CoolingDown(name)
}

// noteRateLimit parks the provider in the shared bucket on
// metadata.ErrRateLimited, mirroring the canonical enrichment path (AGENTS §8).
func (s *Service) noteRateLimit(name string, err error) {
	if s.rateLimit == nil || !errors.Is(err, metadata.ErrRateLimited) {
		return
	}
	s.rateLimit.MarkAfter(name, retryAfterOf(err))
	s.log.Warn("discovery provider rate limited, cooling down", "provider", name, "error", err, "component", "tracking")
}

// retryAfterOf extracts the server-requested backoff from a rate-limit error
// via the same metadata.RateLimitError the canonical path uses; 0 for plain
// sentinels.
func retryAfterOf(err error) time.Duration {
	var rl *metadata.RateLimitError
	if errors.As(err, &rl) {
		return rl.RetryAfter
	}
	return 0
}

func (s *Service) cfg() config.Config {
	if s.cfgFn == nil {
		return config.DefaultConfig()
	}
	return s.cfgFn()
}
