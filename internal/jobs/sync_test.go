package jobs

import (
	"context"
	"errors"
	"testing"
)

type fakePlaylistSyncer struct {
	syncedID int64
	started  bool
	err      error
}

func (f *fakePlaylistSyncer) SyncPlaylistGuarded(_ context.Context, playlistID int64) (bool, error) {
	f.syncedID = playlistID
	return f.started, f.err
}

func TestSyncPlaylistRunner(t *testing.T) {
	syncer := &fakePlaylistSyncer{started: true}
	r := NewRunners(RunnerDeps{Playlist: syncer})

	err := r.SyncPlaylist(42)(context.Background(), func(Report) {})
	if err != nil {
		t.Fatalf("runner error: %v", err)
	}
	if syncer.syncedID != 42 {
		t.Errorf("synced playlist ID = %d, want 42", syncer.syncedID)
	}
}

func TestSyncPlaylistRunnerPropagatesError(t *testing.T) {
	want := errors.New("upstream gone")
	r := NewRunners(RunnerDeps{Playlist: &fakePlaylistSyncer{started: true, err: want}})

	err := r.SyncPlaylist(7)(context.Background(), func(Report) {})
	if !errors.Is(err, want) {
		t.Errorf("runner error = %v, want %v", err, want)
	}
}

func TestSyncPlaylistRunnerAlreadyInProgress(t *testing.T) {
	r := NewRunners(RunnerDeps{Playlist: &fakePlaylistSyncer{started: false}})
	err := r.SyncPlaylist(7)(context.Background(), func(Report) {})
	if err == nil {
		t.Fatal("expected error when a sync for the playlist is already running")
	}
}

func TestSyncPlaylistRunnerNilService(t *testing.T) {
	r := NewRunners(RunnerDeps{})
	err := r.SyncPlaylist(1)(context.Background(), func(Report) {})
	if err == nil {
		t.Fatal("expected error when playlist service is nil")
	}
}
