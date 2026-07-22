package quality

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// ProfileStore defines the interface for quality profile persistence.
type ProfileStore interface {
	Create(ctx context.Context, profile *QualityProfile) (int64, error)
	GetByID(ctx context.Context, id int64) (*QualityProfile, error)
	List(ctx context.Context) ([]QualityProfile, error)
	Update(ctx context.Context, profile *QualityProfile) error
	Delete(ctx context.Context, id int64) error
	SetDefault(ctx context.Context, id int64) error
	// LoadProfileByID resolves a profile by id. nil id → default profile.
	// Returns error if no profile exists.
	LoadProfileByID(ctx context.Context, id *int64) (*QualityProfile, error)
}

// SQLiteProfileStore implements ProfileStore using SQLite.
type SQLiteProfileStore struct {
	db *sql.DB
}

// NewSQLiteProfileStore creates a new store backed by the given DB connection.
func NewSQLiteProfileStore(db *sql.DB) *SQLiteProfileStore {
	return &SQLiteProfileStore{db: db}
}

const profileSelect = `SELECT id, name, description, ranked_targets,
	fallback_enabled, search_mode, rank_candidates_by_quality,
	upgrade_policy, upgrade_cutoff_index, replace_lower_quality,
	is_default, created_at, updated_at
	FROM quality_profiles`

const timeLayout = "2006-01-02 15:04:05"

// Create inserts a new quality profile and returns the new ID.
func (s *SQLiteProfileStore) Create(ctx context.Context, profile *QualityProfile) (int64, error) {
	targetsJSON, err := json.Marshal(profile.RankedTargets)
	if err != nil {
		return 0, fmt.Errorf("create profile: marshal ranked_targets: %w", err)
	}
	if targetsJSON == nil {
		targetsJSON = []byte("[]")
	}

	fallback := boolToInt(profile.FallbackEnabled)
	rankByQuality := boolToInt(profile.RankCandidatesByQuality)
	replace := boolToInt(profile.ReplaceLowerQuality)
	isDefault := boolToInt(profile.IsDefault)

	result, err := s.db.ExecContext(ctx, `
		INSERT INTO quality_profiles (name, description, ranked_targets,
			fallback_enabled, search_mode, rank_candidates_by_quality,
			upgrade_policy, upgrade_cutoff_index, replace_lower_quality, is_default)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		profile.Name, profile.Description, string(targetsJSON),
		fallback, profile.SearchMode, rankByQuality,
		profile.UpgradePolicy, profile.UpgradeCutoffIndex, replace, isDefault,
	)
	if err != nil {
		return 0, fmt.Errorf("create profile: %w", err)
	}
	return result.LastInsertId()
}

// GetByID returns a profile by its ID, or nil if not found.
func (s *SQLiteProfileStore) GetByID(ctx context.Context, id int64) (*QualityProfile, error) {
	row := s.db.QueryRowContext(ctx, profileSelect+" WHERE id=?", id)
	return scanProfile(row)
}

// List returns all profiles ordered by is_default DESC.
func (s *SQLiteProfileStore) List(ctx context.Context) ([]QualityProfile, error) {
	rows, err := s.db.QueryContext(ctx, profileSelect+" ORDER BY is_default DESC")
	if err != nil {
		return nil, fmt.Errorf("list profiles: %w", err)
	}
	defer rows.Close()
	return scanProfiles(rows)
}

// Update patches the profile fields and sets updated_at.
// Only non-zero fields are included in the SET clause to avoid
// zeroing out fields the caller did not provide.
func (s *SQLiteProfileStore) Update(ctx context.Context, profile *QualityProfile) error {
	var sets []string
	var args []any

	if profile.Name != "" {
		sets = append(sets, "name = ?")
		args = append(args, profile.Name)
	}
	if profile.Description != "" {
		sets = append(sets, "description = ?")
		args = append(args, profile.Description)
	}
	if profile.RankedTargets != nil {
		targetsJSON, err := json.Marshal(profile.RankedTargets)
		if err != nil {
			return fmt.Errorf("update profile: marshal ranked_targets: %w", err)
		}
		sets = append(sets, "ranked_targets = ?")
		args = append(args, string(targetsJSON))
	}
	// Boolean fields: always include since Go zero value (false) is ambiguous.
	sets = append(sets, "fallback_enabled = ?")
	args = append(args, boolToInt(profile.FallbackEnabled))
	if profile.SearchMode != "" {
		sets = append(sets, "search_mode = ?")
		args = append(args, profile.SearchMode)
	}
	sets = append(sets, "rank_candidates_by_quality = ?")
	args = append(args, boolToInt(profile.RankCandidatesByQuality))
	if profile.UpgradePolicy != "" {
		sets = append(sets, "upgrade_policy = ?")
		args = append(args, profile.UpgradePolicy)
	}
	sets = append(sets, "upgrade_cutoff_index = ?")
	args = append(args, profile.UpgradeCutoffIndex)
	sets = append(sets, "replace_lower_quality = ?")
	args = append(args, boolToInt(profile.ReplaceLowerQuality))

	// Always bump updated_at.
	sets = append(sets, "updated_at = datetime('now')")

	if len(sets) == 0 {
		return fmt.Errorf("update profile %d: no fields to update", profile.ID)
	}

	query := "UPDATE quality_profiles SET "
	for i, s := range sets {
		if i > 0 {
			query += ", "
		}
		query += s
	}
	query += " WHERE id = ?"
	args = append(args, profile.ID)

	_, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("update profile %d: %w", profile.ID, err)
	}
	return nil
}

// Delete removes a profile and nullifies references in tracks and downloads.
func (s *SQLiteProfileStore) Delete(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete profile %d: %w", id, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`UPDATE tracks SET quality_profile_id = NULL WHERE quality_profile_id = ?`, id); err != nil {
		return fmt.Errorf("delete profile %d: nullify tracks: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE downloads SET quality_profile_id = NULL WHERE quality_profile_id = ?`, id); err != nil {
		return fmt.Errorf("delete profile %d: nullify downloads: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM quality_profiles WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete profile %d: delete row: %w", id, err)
	}
	return tx.Commit()
}

// SetDefault clears any existing default and sets the given profile as default.
func (s *SQLiteProfileStore) SetDefault(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("set default profile %d: %w", id, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`UPDATE quality_profiles SET is_default = 0 WHERE is_default = 1`); err != nil {
		return fmt.Errorf("set default profile %d: clear old default: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE quality_profiles SET is_default = 1, updated_at = datetime('now') WHERE id = ?`, id); err != nil {
		return fmt.Errorf("set default profile %d: set new default: %w", id, err)
	}
	return tx.Commit()
}

// LoadProfileByID resolves a profile by id.
// nil id → returns the default profile (is_default=1).
// non-nil id → fetches by id; falls back to default if not found.
// Returns error if no profile exists at all.
func (s *SQLiteProfileStore) LoadProfileByID(ctx context.Context, id *int64) (*QualityProfile, error) {
	if id == nil {
		// Resolve default profile.
		row := s.db.QueryRowContext(ctx, profileSelect+" WHERE is_default = 1")
		p, err := scanProfile(row)
		if err != nil {
			return nil, fmt.Errorf("load default profile: %w", err)
		}
		if p == nil {
			return nil, fmt.Errorf("load default profile: no quality profiles exist")
		}
		return p, nil
	}

	// Try by exact ID first.
	p, err := s.GetByID(ctx, *id)
	if err != nil {
		return nil, err
	}
	if p != nil {
		return p, nil
	}

	// Not found — fallback to default.
	row := s.db.QueryRowContext(ctx, profileSelect+" WHERE is_default = 1")
	p, err = scanProfile(row)
	if err != nil {
		return nil, fmt.Errorf("load profile %d: fallback to default: %w", *id, err)
	}
	if p == nil {
		return nil, fmt.Errorf("load profile %d: not found and no default profile exists", *id)
	}
	return p, nil
}

// ─── Scan helpers ─────────────────────────────────────────────────────

func scanProfile(row *sql.Row) (*QualityProfile, error) {
	var p QualityProfile
	var targetsJSON string
	var fallback, rankByQuality, replace, isDefault int
	var createdAt, updatedAt string
	err := row.Scan(&p.ID, &p.Name, &p.Description, &targetsJSON,
		&fallback, &p.SearchMode, &rankByQuality,
		&p.UpgradePolicy, &p.UpgradeCutoffIndex, &replace,
		&isDefault, &createdAt, &updatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scan profile: %w", err)
	}
	if targetsJSON != "" {
		json.Unmarshal([]byte(targetsJSON), &p.RankedTargets)
	}
	if p.RankedTargets == nil {
		p.RankedTargets = RankedTargets{}
	}
	p.FallbackEnabled = fallback != 0
	p.RankCandidatesByQuality = rankByQuality != 0
	p.ReplaceLowerQuality = replace != 0
	p.IsDefault = isDefault != 0
	p.CreatedAt, _ = time.Parse(timeLayout, createdAt)
	p.UpdatedAt, _ = time.Parse(timeLayout, updatedAt)
	return &p, nil
}

func scanProfiles(rows *sql.Rows) ([]QualityProfile, error) {
	var profiles []QualityProfile
	for rows.Next() {
		var p QualityProfile
		var targetsJSON string
		var fallback, rankByQuality, replace, isDefault int
		var createdAt, updatedAt string
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &targetsJSON,
			&fallback, &p.SearchMode, &rankByQuality,
			&p.UpgradePolicy, &p.UpgradeCutoffIndex, &replace,
			&isDefault, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan profiles: %w", err)
		}
		if targetsJSON != "" {
			json.Unmarshal([]byte(targetsJSON), &p.RankedTargets)
		}
		if p.RankedTargets == nil {
			p.RankedTargets = RankedTargets{}
		}
		p.FallbackEnabled = fallback != 0
		p.RankCandidatesByQuality = rankByQuality != 0
		p.ReplaceLowerQuality = replace != 0
		p.IsDefault = isDefault != 0
		p.CreatedAt, _ = time.Parse(timeLayout, createdAt)
		p.UpdatedAt, _ = time.Parse(timeLayout, updatedAt)
		profiles = append(profiles, p)
	}
	return profiles, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
