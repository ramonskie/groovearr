package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/ramonskie/groovearr/internal/quality"
)

// ─── Quality Profile handlers ────────────────────────────────────────

// handleListQualityProfiles returns all quality profiles, default first.
func (s *Server) handleListQualityProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := s.qualityProfileStore.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if profiles == nil {
		profiles = []quality.QualityProfile{}
	}
	writeJSON(w, http.StatusOK, profiles)
}

// handleGetQualityProfile returns a single profile by ID.
func (s *Server) handleGetQualityProfile(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid id: %s", idStr))
		return
	}
	profile, err := s.qualityProfileStore.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if profile == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("profile %d not found", id))
		return
	}
	writeJSON(w, http.StatusOK, profile)
}

// handleCreateQualityProfile creates a new quality profile.
func (s *Server) handleCreateQualityProfile(w http.ResponseWriter, r *http.Request) {
	var p quality.QualityProfile
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if p.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	id, err := s.qualityProfileStore.Create(r.Context(), &p)
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "unique") {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	p.ID = id
	writeJSON(w, http.StatusCreated, p)
}

// handleUpdateQualityProfile updates an existing quality profile.
func (s *Server) handleUpdateQualityProfile(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid id: %s", idStr))
		return
	}
	var p quality.QualityProfile
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	// Load the existing profile to avoid zeroing out fields not sent in the request.
	existing, err := s.qualityProfileStore.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("profile %d not found", id))
		return
	}

	// Merge: apply only non-zero fields from the update payload.
	if p.Name != "" {
		existing.Name = p.Name
	}
	if p.Description != "" {
		existing.Description = p.Description
	}
	if len(p.RankedTargets) > 0 {
		existing.RankedTargets = p.RankedTargets
	}
	// Booleans can't be distinguished from zero value in JSON (false == omitted).
	// The frontend sends all boolean fields explicitly, so safe to overwrite.
	existing.FallbackEnabled = p.FallbackEnabled
	existing.RankCandidatesByQuality = p.RankCandidatesByQuality
	existing.ReplaceLowerQuality = p.ReplaceLowerQuality
	if p.SearchMode != "" {
		existing.SearchMode = p.SearchMode
	}
	if p.UpgradePolicy != "" {
		existing.UpgradePolicy = p.UpgradePolicy
	}
	existing.UpgradeCutoffIndex = p.UpgradeCutoffIndex

	if err := s.qualityProfileStore.Update(r.Context(), existing); err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "unique") {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	updated, err := s.qualityProfileStore.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// handleDeleteQualityProfile deletes a profile and nullifies references.
func (s *Server) handleDeleteQualityProfile(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid id: %s", idStr))
		return
	}
	if err := s.qualityProfileStore.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleSetDefaultQualityProfile makes a profile the app-wide default.
func (s *Server) handleSetDefaultQualityProfile(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid id: %s", idStr))
		return
	}
	if err := s.qualityProfileStore.SetDefault(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "default_set"})
}

// qualityPresets holds built-in quality profile configurations.
var qualityPresets = map[string]quality.QualityProfile{
	"audiophile": {
		Name:                    "Audiophile",
		Description:             "FLAC 24-bit preferred, no lossy fallback",
		SearchMode:              quality.SearchBestQuality,
		RankCandidatesByQuality: true,
		RankedTargets: quality.RankedTargets{
			{Label: "FLAC 24-bit/192kHz", Format: "flac", MinBitDepth: 24, MinSampleRate: 192000},
			{Label: "FLAC 24-bit/96kHz", Format: "flac", MinBitDepth: 24, MinSampleRate: 96000},
			{Label: "FLAC 24-bit/48kHz", Format: "flac", MinBitDepth: 24, MinSampleRate: 48000},
			{Label: "FLAC 16-bit", Format: "flac", MinBitDepth: 16},
		},
		FallbackEnabled: false,
		UpgradePolicy:   quality.UpgradeUntilTop,
	},
	"balanced": {
		Name:                    "Balanced",
		Description:             "FLAC preferred, MP3 320 fallback",
		SearchMode:              quality.SearchPriority,
		RankCandidatesByQuality: false,
		RankedTargets: quality.RankedTargets{
			{Label: "FLAC 24-bit/96kHz", Format: "flac", MinBitDepth: 24, MinSampleRate: 96000},
			{Label: "FLAC 16-bit", Format: "flac", MinBitDepth: 16},
			{Label: "MP3 320kbps", Format: "mp3", MinBitrate: 320},
		},
		FallbackEnabled:    true,
		UpgradePolicy:      quality.UpgradeUntilCutoff,
		UpgradeCutoffIndex: 1,
	},
	"space_saver": {
		Name:                    "Space Saver",
		Description:             "MP3 320 preferred, no FLAC",
		SearchMode:              quality.SearchPriority,
		RankCandidatesByQuality: false,
		RankedTargets: quality.RankedTargets{
			{Label: "MP3 320kbps", Format: "mp3", MinBitrate: 320},
			{Label: "MP3 192kbps", Format: "mp3", MinBitrate: 192},
		},
		FallbackEnabled: true,
		UpgradePolicy:   quality.UpgradeAcceptable,
	},
}

// handleQualityProfilePresets returns built-in quality profile presets.
func (s *Server) handleQualityProfilePresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, qualityPresets)
}

// handleApplyQualityProfilePreset applies a named preset to the current default profile.
func (s *Server) handleApplyQualityProfilePreset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Preset string `json:"preset"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	preset, ok := qualityPresets[req.Preset]
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Errorf("unknown preset: %s", req.Preset))
		return
	}

	// Load the current default profile.
	defaultProfile, err := s.qualityProfileStore.LoadProfileByID(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// Apply preset fields to the default profile.
	defaultProfile.Name = preset.Name
	defaultProfile.Description = preset.Description
	defaultProfile.RankedTargets = preset.RankedTargets
	defaultProfile.FallbackEnabled = preset.FallbackEnabled
	defaultProfile.SearchMode = preset.SearchMode
	defaultProfile.RankCandidatesByQuality = preset.RankCandidatesByQuality
	defaultProfile.ReplaceLowerQuality = preset.ReplaceLowerQuality
	defaultProfile.UpgradePolicy = preset.UpgradePolicy
	defaultProfile.UpgradeCutoffIndex = preset.UpgradeCutoffIndex

	if err := s.qualityProfileStore.Update(r.Context(), defaultProfile); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "preset_applied", "preset": req.Preset})
}
