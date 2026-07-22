package download

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/quality"
)

// UpgradeCandidate represents a library track that could be upgraded.
type UpgradeCandidate struct {
	TrackID        int64  `json:"track_id"`
	Title          string `json:"title"`
	Artist         string `json:"artist"`
	CurrentFormat  string `json:"current_format"`
	CurrentBitrate int    `json:"current_bitrate"`
	TargetLabel    string `json:"target_label"` // which ranked target it should reach
	ProfileID      int64  `json:"profile_id"`
	ProfileName    string `json:"profile_name"`
	Reason         string `json:"reason"` // human-readable explanation
}

// LibraryTrackReader is the minimal library access needed by the upgrade scanner.
type LibraryTrackReader interface {
	ListTracksWithQuality(ctx context.Context) ([]domain.Track, error)
}

// trackFormat derives the format string from a file path extension.
func trackFormat(path string) string {
	ext := strings.TrimPrefix(filepath.Ext(path), ".")
	return strings.ToLower(ext)
}

// UpgradeScanner checks library tracks against their quality profiles.
type UpgradeScanner struct {
	log *slog.Logger
}

// NewUpgradeScanner creates a new upgrade scanner.
func NewUpgradeScanner(logger *slog.Logger) *UpgradeScanner {
	if logger == nil {
		logger = slog.Default()
	}
	return &UpgradeScanner{log: logger}
}

// ScanLibrary checks all library tracks against their assigned quality profiles.
// Returns tracks that are below their profile's cutoff target.
func (s *UpgradeScanner) ScanLibrary(
	ctx context.Context,
	profileStore quality.ProfileStore,
	libraryReader LibraryTrackReader,
) ([]UpgradeCandidate, error) {
	tracks, err := libraryReader.ListTracksWithQuality(ctx)
	if err != nil {
		return nil, fmt.Errorf("upgrade scan: list tracks: %w", err)
	}

	// Cache profiles by ID to avoid repeated DB loads.
	profileCache := make(map[int64]*quality.QualityProfile)

	var candidates []UpgradeCandidate
	for _, t := range tracks {
		profileID := int64(0)
		if t.QualityProfileID != nil {
			profileID = *t.QualityProfileID
		}

		profile, ok := profileCache[profileID]
		if !ok {
			var loadErr error
			profile, loadErr = profileStore.LoadProfileByID(ctx, t.QualityProfileID)
			if loadErr != nil {
				s.log.Warn("upgrade scan: load profile failed",
					"track_id", t.ID,
					"profile_id", profileID,
					"error", loadErr,
				)
				continue
			}
			profileCache[profileID] = profile
		}

		// Build AudioQuality from track data.
		aq := quality.AudioQuality{
			Format:  trackFormat(t.FilePath),
			Bitrate: t.Bitrate,
		}

		// Determine if upgrade is needed based on policy.
		needsUpgrade, targetLabel, reason := checkUpgradeNeeded(aq, profile)
		if needsUpgrade {
			candidates = append(candidates, UpgradeCandidate{
				TrackID:        t.ID,
				Title:          t.Title,
				CurrentFormat:  aq.Format,
				CurrentBitrate: aq.Bitrate,
				TargetLabel:    targetLabel,
				ProfileID:      profile.ID,
				ProfileName:    profile.Name,
				Reason:         reason,
			})
		}
	}

	return candidates, nil
}

// checkUpgradeNeeded determines if the current quality needs upgrading per the profile.
func checkUpgradeNeeded(aq quality.AudioQuality, profile *quality.QualityProfile) (needsUpgrade bool, targetLabel string, reason string) {
	targets := profile.RankedTargets
	if len(targets) == 0 {
		return false, "", ""
	}

	// Find which target the current track matches (best = lowest index).
	matchedIdx := -1
	for i, t := range targets {
		if aq.MatchesTarget(t) {
			matchedIdx = i
			break
		}
	}

	switch profile.UpgradePolicy {
	case quality.UpgradeAcceptable:
		// Track is fine as long as it matches ANY target.
		if matchedIdx >= 0 {
			return false, "", ""
		}
		// Doesn't match any target AND fallback disabled → needs upgrade to first target.
		if !profile.FallbackEnabled {
			return true, targets[0].Label, fmt.Sprintf("does not meet minimum target '%s'", targets[0].Label)
		}
		return false, "", ""

	case quality.UpgradeUntilCutoff:
		cutoffIdx := profile.UpgradeCutoffIndex
		if cutoffIdx >= len(targets) {
			cutoffIdx = len(targets) - 1
		}
		// Track needs upgrade if it matches a target BELOW the cutoff (higher index)
		// OR doesn't match any target.
		if matchedIdx < 0 {
			return true, targets[cutoffIdx].Label,
				fmt.Sprintf("does not meet cutoff target '%s'", targets[cutoffIdx].Label)
		}
		if matchedIdx > cutoffIdx {
			return true, targets[cutoffIdx].Label,
				fmt.Sprintf("current '%s' is below cutoff '%s'", targets[matchedIdx].Label, targets[cutoffIdx].Label)
		}
		return false, "", ""

	case quality.UpgradeUntilTop:
		// Track needs upgrade unless at target index 0 (top).
		if matchedIdx == 0 {
			return false, "", ""
		}
		topLabel := targets[0].Label
		if matchedIdx < 0 {
			return true, topLabel,
				fmt.Sprintf("does not meet top target '%s'", topLabel)
		}
		return true, topLabel,
			fmt.Sprintf("current '%s' is below top target '%s'", targets[matchedIdx].Label, topLabel)

	default:
		return false, "", ""
	}
}
