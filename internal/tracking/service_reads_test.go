package tracking

import (
	"context"
	"errors"
	"testing"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/domain"
)

// failingStore embeds a real Store and injects errors for the two read methods
// under test, so the service's error wrapping is exercised without a real DB.
// A nil injected error falls through to the embedded store.
type failingStore struct {
	Store
	getArtistErr  error
	listAlbumsErr error
	listWantedErr error
}

func (f failingStore) GetTrackedArtist(ctx context.Context, id int64) (*domain.TrackedArtist, error) {
	if f.getArtistErr != nil {
		return nil, f.getArtistErr
	}
	return f.Store.GetTrackedArtist(ctx, id)
}

func (f failingStore) ListTrackedAlbums(ctx context.Context, artistID int64) ([]domain.TrackedAlbum, error) {
	if f.listAlbumsErr != nil {
		return nil, f.listAlbumsErr
	}
	return f.Store.ListTrackedAlbums(ctx, artistID)
}

func (f failingStore) ListWantedAlbums(ctx context.Context) ([]domain.TrackedAlbum, error) {
	if f.listWantedErr != nil {
		return nil, f.listWantedErr
	}
	return f.Store.ListWantedAlbums(ctx)
}

func TestGetTrackedArtist(t *testing.T) {
	boom := errors.New("store unavailable")

	tests := []struct {
		name     string
		seed     bool
		storeErr error
		wantNil  bool
		wantErr  bool
		wantName string
	}{
		{name: "found returns the artist", seed: true, wantName: "Tool"},
		{name: "not found returns nil,nil", wantNil: true},
		{name: "store error is wrapped and propagated", storeErr: boom, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newMockStore()
			var artistID int64 = 42
			if tc.seed {
				artistID = seedTrackedArtist(t, store, "Tool")
			}
			var dep Store = store
			if tc.storeErr != nil {
				dep = failingStore{Store: store, getArtistErr: tc.storeErr}
			}
			svc := newTestService(t, dep, newMockLibraryStore(), nil, nil, config.DefaultConfig())

			got, err := svc.GetTrackedArtist(context.Background(), artistID)

			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				if !errors.Is(err, boom) {
					t.Fatalf("error = %v, want wrapped %v", err, boom)
				}
				return
			}
			if err != nil {
				t.Fatalf("GetTrackedArtist: %v", err)
			}
			if tc.wantNil {
				if got != nil {
					t.Fatalf("got %+v, want nil", got)
				}
				return
			}
			if got == nil || got.Name != tc.wantName {
				t.Fatalf("got %+v, want name %q", got, tc.wantName)
			}
		})
	}
}

func TestListAlbums(t *testing.T) {
	boom := errors.New("store unavailable")

	tests := []struct {
		name      string
		seedAlbum bool
		storeErr  error
		wantLen   int
		wantTitle string
		wantErr   bool
	}{
		{name: "found returns the albums", seedAlbum: true, wantLen: 1, wantTitle: "Lateralus"},
		{name: "artist with no albums returns an empty slice", wantLen: 0},
		{name: "store error is wrapped and propagated", storeErr: boom, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newMockStore()
			artistID := seedTrackedArtist(t, store, "Tool")
			if tc.seedAlbum {
				seedTrackedAlbum(t, store, artistID, "a1", "Lateralus", domain.AlbumStatusWanted, true)
			}
			var dep Store = store
			if tc.storeErr != nil {
				dep = failingStore{Store: store, listAlbumsErr: tc.storeErr}
			}
			svc := newTestService(t, dep, newMockLibraryStore(), nil, nil, config.DefaultConfig())

			got, err := svc.ListAlbums(context.Background(), artistID)

			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				if !errors.Is(err, boom) {
					t.Fatalf("error = %v, want wrapped %v", err, boom)
				}
				return
			}
			if err != nil {
				t.Fatalf("ListAlbums: %v", err)
			}
			if len(got) != tc.wantLen {
				t.Fatalf("len(albums) = %d, want %d", len(got), tc.wantLen)
			}
			if tc.wantTitle != "" && got[0].Title != tc.wantTitle {
				t.Fatalf("title = %q, want %q", got[0].Title, tc.wantTitle)
			}
		})
	}
}

func TestListAllWanted(t *testing.T) {
	boom := errors.New("store unavailable")

	tests := []struct {
		name      string
		seedAlbum bool
		storeErr  error
		wantLen   int
		wantTitle string
		wantErr   bool
	}{
		{name: "found returns the wanted albums", seedAlbum: true, wantLen: 1, wantTitle: "Lateralus"},
		{name: "no wanted albums returns an empty slice", wantLen: 0},
		{name: "store error is wrapped and propagated", storeErr: boom, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newMockStore()
			artistID := seedTrackedArtist(t, store, "Tool")
			if tc.seedAlbum {
				seedTrackedAlbum(t, store, artistID, "a1", "Lateralus", domain.AlbumStatusWanted, true)
			}
			var dep Store = store
			if tc.storeErr != nil {
				dep = failingStore{Store: store, listWantedErr: tc.storeErr}
			}
			svc := newTestService(t, dep, newMockLibraryStore(), nil, nil, config.DefaultConfig())

			got, err := svc.ListAllWanted(context.Background())

			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				if !errors.Is(err, boom) {
					t.Fatalf("error = %v, want wrapped %v", err, boom)
				}
				return
			}
			if err != nil {
				t.Fatalf("ListAllWanted: %v", err)
			}
			if len(got) != tc.wantLen {
				t.Fatalf("len(albums) = %d, want %d", len(got), tc.wantLen)
			}
			if tc.wantTitle != "" && got[0].Title != tc.wantTitle {
				t.Fatalf("title = %q, want %q", got[0].Title, tc.wantTitle)
			}
		})
	}
}
