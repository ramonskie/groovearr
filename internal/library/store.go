// Package library provides the music library database operations.
package library

import (
	"context"

	"github.com/ramonskie/groovearr/internal/domain"
)

// Store is the interface for music library persistence.
// Implementations: SQLite (current), potentially PostgreSQL in the future.
type Store interface {
	// Artists.
	UpsertArtist(ctx context.Context, artist *domain.Artist) (int64, error)
	GetArtist(ctx context.Context, id int64) (*domain.Artist, error)
	GetArtistByName(ctx context.Context, name string) (*domain.Artist, error)
	ListArtists(ctx context.Context, offset, limit int) ([]domain.Artist, error)
	SearchArtists(ctx context.Context, query string, limit int) ([]domain.Artist, error)

	// Albums.
	UpsertAlbum(ctx context.Context, album *domain.Album) (int64, error)
	GetAlbum(ctx context.Context, id int64) (*domain.Album, error)
	GetAlbumsByArtist(ctx context.Context, artistID int64) ([]domain.Album, error)
	SearchAlbums(ctx context.Context, query string, limit int) ([]domain.Album, error)

	// Tracks.
	UpsertTrack(ctx context.Context, track *domain.Track) (int64, error)
	GetTrack(ctx context.Context, id int64) (*domain.Track, error)
	GetTracksByAlbum(ctx context.Context, albumID int64) ([]domain.Track, error)
	GetTracksByArtist(ctx context.Context, artistID int64) ([]domain.Track, error)
	SearchTracks(ctx context.Context, query string, limit int) ([]domain.Track, error)
	GetTrackByFilePath(ctx context.Context, filePath string) (*domain.Track, error)
	DeleteTrack(ctx context.Context, id int64) error

	// ImportTrack creates artist/album/track records for a single file in
	// a single atomic pipeline, returning the new track ID. Used by both
	// the filesystem scanner and the download import handler.
	ImportTrack(ctx context.Context, track *domain.Track, artistName, albumTitle string, albumYear int, genres []string) (int64, error)

	// External ID lookups.
	GetArtistByExternalID(ctx context.Context, service, externalID string) (*domain.Artist, error)
	GetAlbumByExternalID(ctx context.Context, service, externalID string) (*domain.Album, error)
	GetTrackByExternalID(ctx context.Context, service, externalID string) (*domain.Track, error)

	// Playlists.
	UpsertPlaylist(ctx context.Context, p *domain.Playlist) (int64, error)
	GetPlaylist(ctx context.Context, id int64) (*domain.Playlist, error)
	GetPlaylistBySourceID(ctx context.Context, source, sourceID string) (*domain.Playlist, error)
	ListPlaylists(ctx context.Context) ([]domain.Playlist, error)
	DeletePlaylist(ctx context.Context, id int64) error

	UpsertPlaylistTrack(ctx context.Context, t *domain.PlaylistTrack) error
	GetPlaylistTracks(ctx context.Context, playlistID int64) ([]domain.PlaylistTrack, error)
	DeletePlaylistTracks(ctx context.Context, playlistID int64) error

	// Maintenance.
	Close() error
}
