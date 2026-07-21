package spotify

import (
	"encoding/json"
	"testing"
)

// ─── Helpers ─────────────────────────────────────────────────────────

func ptr[T any](v T) *T { return &v }

// ─── Track unmarshal ─────────────────────────────────────────────────

func TestUnmarshalTrack(t *testing.T) {
	data := []byte(`{
		"album": {
			"album_type": "album",
			"total_tracks": 9,
			"external_urls": { "spotify": "https://open.spotify.com/album/0sNOF9WDwhWunNAHPD3Baj" },
			"href": "https://api.spotify.com/v1/albums/0sNOF9WDwhWunNAHPD3Baj",
			"id": "0sNOF9WDwhWunNAHPD3Baj",
			"images": [
				{ "url": "https://i.scdn.co/image/ab67616d0000b273e2e352d89826aef6dbd5ff8f", "height": 640, "width": 640 }
			],
			"name": "Shepherd's Dog",
			"release_date": "2007-09-25",
			"release_date_precision": "day",
			"type": "album",
			"uri": "spotify:album:0sNOF9WDwhWunNAHPD3Baj",
			"artists": [
				{
					"external_urls": { "spotify": "https://open.spotify.com/artist/2y8JoaoCj6mRrO7SpwAyR" },
					"href": "https://api.spotify.com/v1/artists/2y8JoaoCj6mRrO7SpwAyR",
					"id": "2y8JoaoCj6mRrO7SpwAyR",
					"name": "Iron & Wine",
					"type": "artist",
					"uri": "spotify:artist:2y8JoaoCj6mRrO7SpwAyR"
				}
			]
		},
		"artists": [
			{
				"external_urls": { "spotify": "https://open.spotify.com/artist/2y8JoaoCj6mRrO7SpwAyR" },
				"href": "https://api.spotify.com/v1/artists/2y8JoaoCj6mRrO7SpwAyR",
				"id": "2y8JoaoCj6mRrO7SpwAyR",
				"name": "Iron & Wine",
				"type": "artist",
				"uri": "spotify:artist:2y8JoaoCj6mRrO7SpwAyR"
			}
		],
		"disc_number": 1,
		"duration_ms": 255373,
		"explicit": false,
		"external_ids": { "isrc": "USWB10703871" },
		"external_urls": { "spotify": "https://open.spotify.com/track/7MrfUMfVE33qJdM61HtBgN" },
		"href": "https://api.spotify.com/v1/tracks/7MrfUMfVE33qJdM61HtBgN",
		"id": "7MrfUMfVE33qJdM61HtBgN",
		"name": "Flightless Bird, American Mouth",
		"popularity": 56,
		"preview_url": null,
		"track_number": 5,
		"type": "track",
		"uri": "spotify:track:7MrfUMfVE33qJdM61HtBgN",
		"is_local": false
	}`)

	var track Track
	if err := json.Unmarshal(data, &track); err != nil {
		t.Fatalf("unmarshal Track: %v", err)
	}

	if track.ID != "7MrfUMfVE33qJdM61HtBgN" {
		t.Errorf("ID = %q, want %q", track.ID, "7MrfUMfVE33qJdM61HtBgN")
	}
	if track.Name != "Flightless Bird, American Mouth" {
		t.Errorf("Name = %q", track.Name)
	}
	if track.DurationMs != 255373 {
		t.Errorf("DurationMs = %d, want 255373", track.DurationMs)
	}
	if track.Explicit {
		t.Error("Explicit should be false")
	}
	if track.Popularity != 56 {
		t.Errorf("Popularity = %d, want 56", track.Popularity)
	}
	if track.PreviewURL != nil {
		t.Error("PreviewURL should be nil for null JSON")
	}
	if track.DiscNumber != 1 {
		t.Errorf("DiscNumber = %d, want 1", track.DiscNumber)
	}
	if track.TrackNumber != 5 {
		t.Errorf("TrackNumber = %d, want 5", track.TrackNumber)
	}
	if track.Href != "https://api.spotify.com/v1/tracks/7MrfUMfVE33qJdM61HtBgN" {
		t.Errorf("Href mismatch")
	}
	if track.URI != "spotify:track:7MrfUMfVE33qJdM61HtBgN" {
		t.Errorf("URI mismatch")
	}
	if track.ExternalIDs.ISRC != "USWB10703871" {
		t.Errorf("ExternalIDs.ISRC = %q, want USWB10703871", track.ExternalIDs.ISRC)
	}
	if track.ExternalURLs.Spotify == "" {
		t.Error("ExternalURLs.Spotify should not be empty")
	}

	// Album (SimplifiedAlbum)
	if track.Album.ID != "0sNOF9WDwhWunNAHPD3Baj" {
		t.Errorf("Album.ID = %q", track.Album.ID)
	}
	if track.Album.Name != "Shepherd's Dog" {
		t.Errorf("Album.Name = %q", track.Album.Name)
	}
	if len(track.Album.Images) != 1 {
		t.Errorf("Album.Images len = %d, want 1", len(track.Album.Images))
	}
	if track.Album.Images[0].URL == "" {
		t.Error("Album.Images[0].URL should not be empty")
	}
	if track.Album.Images[0].Height == nil || *track.Album.Images[0].Height != 640 {
		t.Error("Album.Images[0].Height should be 640")
	}

	// Artists
	if len(track.Artists) != 1 {
		t.Fatalf("Artists len = %d, want 1", len(track.Artists))
	}
	if track.Artists[0].Name != "Iron & Wine" {
		t.Errorf("Artists[0].Name = %q", track.Artists[0].Name)
	}
}

// ─── Album unmarshal ─────────────────────────────────────────────────

func TestUnmarshalAlbum(t *testing.T) {
	data := []byte(`{
		"album_type": "album",
		"total_tracks": 10,
		"external_urls": { "spotify": "https://open.spotify.com/album/4LH4d3cOWNNsVw41Gqt2kv" },
		"href": "https://api.spotify.com/v1/albums/4LH4d3cOWNNsVw41Gqt2kv",
		"id": "4LH4d3cOWNNsVw41Gqt2kv",
		"images": [
			{ "url": "https://i.scdn.co/image/ab67616d0000b2731c094d644833c2d3b7abf4a7", "height": 640, "width": 640 }
		],
		"name": "The Dark Side of the Moon",
		"release_date": "1973-03-01",
		"release_date_precision": "day",
		"type": "album",
		"uri": "spotify:album:4LH4d3cOWNNsVw41Gqt2kv",
		"artists": [
			{
				"external_urls": { "spotify": "https://open.spotify.com/artist/0k17h0D3J5VfsdmQ1iZtE9" },
				"href": "https://api.spotify.com/v1/artists/0k17h0D3J5VfsdmQ1iZtE9",
				"id": "0k17h0D3J5VfsdmQ1iZtE9",
				"name": "Pink Floyd",
				"type": "artist",
				"uri": "spotify:artist:0k17h0D3J5VfsdmQ1iZtE9"
			}
		],
		"tracks": {
			"href": "https://api.spotify.com/v1/albums/4LH4d3cOWNNsVw41Gqt2kv/tracks?offset=0&limit=2",
			"limit": 2,
			"next": "https://api.spotify.com/v1/albums/4LH4d3cOWNNsVw41Gqt2kv/tracks?offset=2&limit=2",
			"offset": 0,
			"previous": null,
			"total": 10,
			"items": [
				{
					"artists": [
						{
							"external_urls": { "spotify": "https://open.spotify.com/artist/0k17h0D3J5VfsdmQ1iZtE9" },
							"href": "https://api.spotify.com/v1/artists/0k17h0D3J5VfsdmQ1iZtE9",
							"id": "0k17h0D3J5VfsdmQ1iZtE9",
							"name": "Pink Floyd",
							"type": "artist",
							"uri": "spotify:artist:0k17h0D3J5VfsdmQ1iZtE9"
						}
					],
					"disc_number": 1,
					"duration_ms": 68320,
					"explicit": false,
					"external_urls": { "spotify": "https://open.spotify.com/track/4VIbFCNMxKHKR5uI80JvNs" },
					"href": "https://api.spotify.com/v1/tracks/4VIbFCNMxKHKR5uI80JvNs",
					"id": "4VIbFCNMxKHKR5uI80JvNs",
					"name": "Speak to Me",
					"preview_url": null,
					"track_number": 1,
					"type": "track",
					"uri": "spotify:track:4VIbFCNMxKHKR5uI80JvNs",
					"is_local": false
				}
			]
		},
		"copyrights": [
			{ "text": "2016 Pink Floyd Music Ltd., marketed and distributed by Parlophone Records Ltd.", "type": "C" },
			{ "text": "2016 Pink Floyd Music Ltd., marketed and distributed by Parlophone Records Ltd.", "type": "P" }
		],
		"external_ids": { "upc": "190295827214" },
		"genres": ["Progressive Rock", "Psychedelic Rock"],
		"label": "Parlophone UK",
		"popularity": 81
	}`)

	var album Album
	if err := json.Unmarshal(data, &album); err != nil {
		t.Fatalf("unmarshal Album: %v", err)
	}

	if album.ID != "4LH4d3cOWNNsVw41Gqt2kv" {
		t.Errorf("ID = %q", album.ID)
	}
	if album.Name != "The Dark Side of the Moon" {
		t.Errorf("Name = %q", album.Name)
	}
	if album.AlbumType != "album" {
		t.Errorf("AlbumType = %q", album.AlbumType)
	}
	if album.TotalTracks != 10 {
		t.Errorf("TotalTracks = %d, want 10", album.TotalTracks)
	}
	if album.ReleaseDate != "1973-03-01" {
		t.Errorf("ReleaseDate = %q", album.ReleaseDate)
	}
	if album.ReleaseDatePrecision != "day" {
		t.Errorf("ReleaseDatePrecision = %q", album.ReleaseDatePrecision)
	}
	if album.Label != "Parlophone UK" {
		t.Errorf("Label = %q", album.Label)
	}
	if album.Popularity != 81 {
		t.Errorf("Popularity = %d, want 81", album.Popularity)
	}
	if album.Href != "https://api.spotify.com/v1/albums/4LH4d3cOWNNsVw41Gqt2kv" {
		t.Errorf("Href mismatch")
	}
	if album.URI != "spotify:album:4LH4d3cOWNNsVw41Gqt2kv" {
		t.Errorf("URI mismatch")
	}

	// Images
	if len(album.Images) != 1 {
		t.Errorf("Images len = %d, want 1", len(album.Images))
	}

	// Artists
	if len(album.Artists) != 1 || album.Artists[0].Name != "Pink Floyd" {
		t.Error("Artists mismatch")
	}

	// Genres
	if len(album.Genres) != 2 {
		t.Errorf("Genres len = %d, want 2", len(album.Genres))
	}

	// ExternalIDs
	if album.ExternalIDs.UPC != "190295827214" {
		t.Errorf("ExternalIDs.UPC = %q", album.ExternalIDs.UPC)
	}

	// Copyrights
	if len(album.Copyrights) != 2 {
		t.Errorf("Copyrights len = %d, want 2", len(album.Copyrights))
	}
	if album.Copyrights[0].Type != "C" {
		t.Errorf("Copyrights[0].Type = %q, want C", album.Copyrights[0].Type)
	}

	// Tracks paging
	if album.Tracks.Total != 10 {
		t.Errorf("Tracks.Total = %d, want 10", album.Tracks.Total)
	}
	if album.Tracks.Limit != 2 {
		t.Errorf("Tracks.Limit = %d, want 2", album.Tracks.Limit)
	}
	if album.Tracks.Offset != 0 {
		t.Errorf("Tracks.Offset = %d, want 0", album.Tracks.Offset)
	}
	if album.Tracks.Previous != "" {
		t.Error("Tracks.Previous should be empty (JSON null)")
	}
	if album.Tracks.Next == "" {
		t.Error("Tracks.Next should not be empty")
	}
	if len(album.Tracks.Items) != 1 {
		t.Fatalf("Tracks.Items len = %d, want 1", len(album.Tracks.Items))
	}
	if album.Tracks.Items[0].Name != "Speak to Me" {
		t.Errorf("Tracks.Items[0].Name = %q", album.Tracks.Items[0].Name)
	}
	if album.Tracks.Items[0].DurationMs != 68320 {
		t.Errorf("Tracks.Items[0].DurationMs = %d", album.Tracks.Items[0].DurationMs)
	}
}

// ─── Playlist unmarshal ──────────────────────────────────────────────

func TestUnmarshalPlaylist(t *testing.T) {
	data := []byte(`{
		"collaborative": false,
		"description": "Late night driving music",
		"external_urls": { "spotify": "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M" },
		"followers": { "href": null, "total": 45283947 },
		"href": "https://api.spotify.com/v1/playlists/37i9dQZF1DXcBWIGoYBM5M",
		"id": "37i9dQZF1DXcBWIGoYBM5M",
		"images": [
			{ "url": "https://i.scdn.co/image/ab67706f00000003c00e1e04f72b83aa5809e0e0", "height": null, "width": null }
		],
		"name": "Today's Top Hits",
		"owner": {
			"external_urls": { "spotify": "https://open.spotify.com/user/spotify" },
			"href": "https://api.spotify.com/v1/users/spotify",
			"id": "spotify",
			"type": "user",
			"uri": "spotify:user:spotify",
			"display_name": "Spotify"
		},
		"public": true,
		"snapshot_id": "MTc1MDk0NjAwMCwwMDAwMDAwMA==",
		"tracks": {
			"href": "https://api.spotify.com/v1/playlists/37i9dQZF1DXcBWIGoYBM5M/tracks",
			"limit": 2,
			"next": "https://api.spotify.com/v1/playlists/37i9dQZF1DXcBWIGoYBM5M/tracks?offset=2&limit=2",
			"offset": 0,
			"previous": null,
			"total": 50,
			"items": [
				{
					"added_at": "2025-06-01T12:00:00Z",
					"added_by": {
						"external_urls": { "spotify": "https://open.spotify.com/user/spotify" },
						"href": "https://api.spotify.com/v1/users/spotify",
						"id": "spotify",
						"type": "user",
						"uri": "spotify:user:spotify"
					},
					"is_local": false,
					"track": {
						"album": {
							"album_type": "single",
							"total_tracks": 1,
							"external_urls": { "spotify": "https://open.spotify.com/album/1abc123" },
							"href": "https://api.spotify.com/v1/albums/1abc123",
							"id": "1abc123",
							"images": [],
							"name": "Hit Single",
							"release_date": "2025-05-15",
							"release_date_precision": "day",
							"type": "album",
							"uri": "spotify:album:1abc123",
							"artists": [
								{
									"external_urls": { "spotify": "https://open.spotify.com/artist/art1" },
									"href": "https://api.spotify.com/v1/artists/art1",
									"id": "art1",
									"name": "Popular Artist",
									"type": "artist",
									"uri": "spotify:artist:art1"
								}
							]
						},
						"artists": [
							{
								"external_urls": { "spotify": "https://open.spotify.com/artist/art1" },
								"href": "https://api.spotify.com/v1/artists/art1",
								"id": "art1",
								"name": "Popular Artist",
								"type": "artist",
								"uri": "spotify:artist:art1"
							}
						],
						"disc_number": 1,
						"duration_ms": 210000,
						"explicit": false,
						"external_ids": { "isrc": "US1234567890" },
						"external_urls": { "spotify": "https://open.spotify.com/track/t1" },
						"href": "https://api.spotify.com/v1/tracks/t1",
						"id": "t1",
						"name": "Hit Single",
						"popularity": 95,
						"preview_url": "https://p.scdn.co/mp3-preview/abc123",
						"track_number": 1,
						"type": "track",
						"uri": "spotify:track:t1",
						"is_local": false
					}
				}
			]
		},
		"type": "playlist",
		"uri": "spotify:playlist:37i9dQZF1DXcBWIGoYBM5M"
	}`)

	var pl Playlist
	if err := json.Unmarshal(data, &pl); err != nil {
		t.Fatalf("unmarshal Playlist: %v", err)
	}

	if pl.ID != "37i9dQZF1DXcBWIGoYBM5M" {
		t.Errorf("ID = %q", pl.ID)
	}
	if pl.Name != "Today's Top Hits" {
		t.Errorf("Name = %q", pl.Name)
	}
	if pl.Description == nil || *pl.Description != "Late night driving music" {
		t.Error("Description mismatch")
	}
	if pl.Public == nil || *pl.Public != true {
		t.Error("Public should be true")
	}
	if pl.Collaborative {
		t.Error("Collaborative should be false")
	}
	if pl.SnapshotID != "MTc1MDk0NjAwMCwwMDAwMDAwMA==" {
		t.Errorf("SnapshotID mismatch")
	}
	if pl.Href != "https://api.spotify.com/v1/playlists/37i9dQZF1DXcBWIGoYBM5M" {
		t.Errorf("Href mismatch")
	}
	if pl.URI != "spotify:playlist:37i9dQZF1DXcBWIGoYBM5M" {
		t.Errorf("URI mismatch")
	}

	// Followers
	if pl.Followers.Total != 45283947 {
		t.Errorf("Followers.Total = %d, want 45283947", pl.Followers.Total)
	}
	if pl.Followers.Href != nil {
		t.Error("Followers.Href should be nil")
	}

	// Owner
	if pl.Owner.ID != "spotify" {
		t.Errorf("Owner.ID = %q", pl.Owner.ID)
	}
	if pl.Owner.DisplayName == nil || *pl.Owner.DisplayName != "Spotify" {
		t.Error("Owner.DisplayName mismatch")
	}

	// Images
	if len(pl.Images) != 1 {
		t.Errorf("Images len = %d, want 1", len(pl.Images))
	}

	// Tracks paging
	if pl.Tracks.Total != 50 {
		t.Errorf("Tracks.Total = %d, want 50", pl.Tracks.Total)
	}
	if pl.Tracks.Limit != 2 {
		t.Errorf("Tracks.Limit = %d, want 2", pl.Tracks.Limit)
	}
	if pl.Tracks.Previous != "" {
		t.Error("Tracks.Previous should be empty (JSON null)")
	}

	// PlaylistTrack item
	if len(pl.Tracks.Items) != 1 {
		t.Fatalf("Tracks.Items len = %d, want 1", len(pl.Tracks.Items))
	}
	pt := pl.Tracks.Items[0]
	if pt.IsLocal {
		t.Error("PlaylistTrack.IsLocal should be false")
	}
	if pt.AddedAt == nil || *pt.AddedAt != "2025-06-01T12:00:00Z" {
		t.Error("PlaylistTrack.AddedAt mismatch")
	}
	if pt.AddedBy == nil || pt.AddedBy.ID != "spotify" {
		t.Error("PlaylistTrack.AddedBy mismatch")
	}
	if pt.Track == nil {
		t.Fatal("PlaylistTrack.Track should not be nil")
	}
	if pt.Track.Name != "Hit Single" {
		t.Errorf("PlaylistTrack.Track.Name = %q", pt.Track.Name)
	}
	if pt.Track.PreviewURL == nil || *pt.Track.PreviewURL != "https://p.scdn.co/mp3-preview/abc123" {
		t.Error("PlaylistTrack.Track.PreviewURL mismatch")
	}
}

// ─── SimplifiedPlaylist unmarshal ────────────────────────────────────

func TestUnmarshalSimplifiedPlaylist(t *testing.T) {
	data := []byte(`{
		"collaborative": false,
		"description": null,
		"external_urls": { "spotify": "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M" },
		"href": "https://api.spotify.com/v1/playlists/37i9dQZF1DXcBWIGoYBM5M",
		"id": "37i9dQZF1DXcBWIGoYBM5M",
		"images": [],
		"name": "Today's Top Hits",
		"owner": {
			"external_urls": { "spotify": "https://open.spotify.com/user/spotify" },
			"href": "https://api.spotify.com/v1/users/spotify",
			"id": "spotify",
			"type": "user",
			"uri": "spotify:user:spotify",
			"display_name": "Spotify"
		},
		"public": null,
		"snapshot_id": "abc123",
		"tracks": { "href": "https://api.spotify.com/v1/playlists/x/tracks", "total": 50 },
		"type": "playlist",
		"uri": "spotify:playlist:37i9dQZF1DXcBWIGoYBM5M"
	}`)

	var sp SimplifiedPlaylist
	if err := json.Unmarshal(data, &sp); err != nil {
		t.Fatalf("unmarshal SimplifiedPlaylist: %v", err)
	}

	if sp.ID != "37i9dQZF1DXcBWIGoYBM5M" {
		t.Errorf("ID = %q", sp.ID)
	}
	if sp.Name != "Today's Top Hits" {
		t.Errorf("Name = %q", sp.Name)
	}
	if sp.Description != nil {
		t.Error("Description should be nil for null JSON")
	}
	if sp.Public != nil {
		t.Error("Public should be nil for null JSON")
	}
	if sp.Collaborative {
		t.Error("Collaborative should be false")
	}
	if sp.SnapshotID != "abc123" {
		t.Errorf("SnapshotID = %q", sp.SnapshotID)
	}

	// Tracks ref
	if sp.Tracks == nil {
		t.Fatal("Tracks should not be nil")
	}
	if sp.Tracks.Total != 50 {
		t.Errorf("Tracks.Total = %d, want 50", sp.Tracks.Total)
	}
	if sp.Tracks.Href == "" {
		t.Error("Tracks.Href should not be empty")
	}

	// Empty images array
	if len(sp.Images) != 0 {
		t.Errorf("Images len = %d, want 0", len(sp.Images))
	}
}

// ─── Paging unmarshal ────────────────────────────────────────────────

func TestUnmarshalPaging(t *testing.T) {
	data := []byte(`{
		"href": "https://api.spotify.com/v1/search?query=test&type=track&offset=0&limit=2",
		"limit": 2,
		"next": "https://api.spotify.com/v1/search?query=test&type=track&offset=2&limit=2",
		"offset": 0,
		"previous": null,
		"total": 100,
		"items": [
			{
				"album": {
					"album_type": "album",
					"total_tracks": 12,
					"external_urls": { "spotify": "https://open.spotify.com/album/x" },
					"href": "https://api.spotify.com/v1/albums/x",
					"id": "x",
					"images": [],
					"name": "Album One",
					"release_date": "2020-01-01",
					"release_date_precision": "day",
					"type": "album",
					"uri": "spotify:album:x",
					"artists": []
				},
				"artists": [],
				"disc_number": 1,
				"duration_ms": 200000,
				"explicit": false,
				"external_ids": {},
				"external_urls": { "spotify": "https://open.spotify.com/track/t1" },
				"href": "https://api.spotify.com/v1/tracks/t1",
				"id": "t1",
				"name": "Track One",
				"popularity": 50,
				"preview_url": null,
				"track_number": 1,
				"type": "track",
				"uri": "spotify:track:t1",
				"is_local": false
			},
			{
				"album": {
					"album_type": "single",
					"total_tracks": 1,
					"external_urls": { "spotify": "https://open.spotify.com/album/y" },
					"href": "https://api.spotify.com/v1/albums/y",
					"id": "y",
					"images": [],
					"name": "Single Two",
					"release_date": "2021-06-15",
					"release_date_precision": "day",
					"type": "album",
					"uri": "spotify:album:y",
					"artists": []
				},
				"artists": [],
				"disc_number": 1,
				"duration_ms": 180000,
				"explicit": true,
				"external_ids": {},
				"external_urls": { "spotify": "https://open.spotify.com/track/t2" },
				"href": "https://api.spotify.com/v1/tracks/t2",
				"id": "t2",
				"name": "Track Two",
				"popularity": 30,
				"preview_url": null,
				"track_number": 1,
				"type": "track",
				"uri": "spotify:track:t2",
				"is_local": false
			}
		]
	}`)

	var page Paging[Track]
	if err := json.Unmarshal(data, &page); err != nil {
		t.Fatalf("unmarshal Paging[Track]: %v", err)
	}

	if page.Total != 100 {
		t.Errorf("Total = %d, want 100", page.Total)
	}
	if page.Limit != 2 {
		t.Errorf("Limit = %d, want 2", page.Limit)
	}
	if page.Offset != 0 {
		t.Errorf("Offset = %d, want 0", page.Offset)
	}
	if page.Previous != "" {
		t.Error("Previous should be empty (JSON null)")
	}
	if page.Next == "" {
		t.Error("Next should not be empty")
	}
	if page.Href == "" {
		t.Error("Href should not be empty")
	}

	if len(page.Items) != 2 {
		t.Fatalf("Items len = %d, want 2", len(page.Items))
	}
	if page.Items[0].ID != "t1" {
		t.Errorf("Items[0].ID = %q", page.Items[0].ID)
	}
	if page.Items[0].Name != "Track One" {
		t.Errorf("Items[0].Name = %q", page.Items[0].Name)
	}
	if page.Items[1].ID != "t2" {
		t.Errorf("Items[1].ID = %q", page.Items[1].ID)
	}
	if !page.Items[1].Explicit {
		t.Error("Items[1].Explicit should be true")
	}
}

// ─── Empty paging (last page, no next) ───────────────────────────────

func TestUnmarshalPagingLastPage(t *testing.T) {
	data := []byte(`{
		"href": "https://api.spotify.com/v1/search?query=test&type=track&offset=98&limit=2",
		"limit": 2,
		"next": null,
		"offset": 98,
		"previous": "https://api.spotify.com/v1/search?query=test&type=track&offset=96&limit=2",
		"total": 100,
		"items": []
	}`)

	var page Paging[Track]
	if err := json.Unmarshal(data, &page); err != nil {
		t.Fatalf("unmarshal Paging last page: %v", err)
	}

	if page.Next != "" {
		t.Error("Next should be empty on last page (JSON null)")
	}
	if page.Previous == "" {
		t.Error("Previous should not be empty on last page")
	}
	if len(page.Items) != 0 {
		t.Errorf("Items len = %d, want 0", len(page.Items))
	}
	if page.Total != 100 {
		t.Errorf("Total = %d, want 100", page.Total)
	}
}

// ─── User unmarshal ──────────────────────────────────────────────────

func TestUnmarshalUser(t *testing.T) {
	data := []byte(`{
		"external_urls": { "spotify": "https://open.spotify.com/user/johndoe" },
		"href": "https://api.spotify.com/v1/users/johndoe",
		"id": "johndoe",
		"type": "user",
		"uri": "spotify:user:johndoe",
		"display_name": "John Doe"
	}`)

	var u User
	if err := json.Unmarshal(data, &u); err != nil {
		t.Fatalf("unmarshal User: %v", err)
	}

	if u.ID != "johndoe" {
		t.Errorf("ID = %q", u.ID)
	}
	if u.DisplayName == nil || *u.DisplayName != "John Doe" {
		t.Error("DisplayName mismatch")
	}
	if u.Type != "user" {
		t.Errorf("Type = %q", u.Type)
	}
	if u.ExternalURLs.Spotify == "" {
		t.Error("ExternalURLs.Spotify should not be empty")
	}
}

// ─── User with null display_name ─────────────────────────────────────

func TestUnmarshalUserNullDisplayName(t *testing.T) {
	data := []byte(`{
		"external_urls": { "spotify": "https://open.spotify.com/user/privateuser" },
		"href": "https://api.spotify.com/v1/users/privateuser",
		"id": "privateuser",
		"type": "user",
		"uri": "spotify:user:privateuser",
		"display_name": null
	}`)

	var u User
	if err := json.Unmarshal(data, &u); err != nil {
		t.Fatalf("unmarshal User with null display_name: %v", err)
	}

	if u.DisplayName != nil {
		t.Error("DisplayName should be nil for null JSON")
	}
	if u.ID != "privateuser" {
		t.Errorf("ID = %q", u.ID)
	}
}

// ─── Artist unmarshal ────────────────────────────────────────────────

func TestUnmarshalArtist(t *testing.T) {
	data := []byte(`{
		"external_urls": { "spotify": "https://open.spotify.com/artist/0k17h0D3J5VfsdmQ1iZtE9" },
		"followers": { "href": null, "total": 10500000 },
		"genres": ["Progressive Rock", "Psychedelic Rock"],
		"href": "https://api.spotify.com/v1/artists/0k17h0D3J5VfsdmQ1iZtE9",
		"id": "0k17h0D3J5VfsdmQ1iZtE9",
		"images": [
			{ "url": "https://i.scdn.co/image/artist1", "height": 640, "width": 640 }
		],
		"name": "Pink Floyd",
		"popularity": 82,
		"type": "artist",
		"uri": "spotify:artist:0k17h0D3J5VfsdmQ1iZtE9"
	}`)

	var a Artist
	if err := json.Unmarshal(data, &a); err != nil {
		t.Fatalf("unmarshal Artist: %v", err)
	}

	if a.ID != "0k17h0D3J5VfsdmQ1iZtE9" {
		t.Errorf("ID = %q", a.ID)
	}
	if a.Name != "Pink Floyd" {
		t.Errorf("Name = %q", a.Name)
	}
	if a.Popularity != 82 {
		t.Errorf("Popularity = %d, want 82", a.Popularity)
	}
	if a.Followers.Total != 10500000 {
		t.Errorf("Followers.Total = %d", a.Followers.Total)
	}
	if a.Followers.Href != nil {
		t.Error("Followers.Href should be nil")
	}
	if len(a.Genres) != 2 {
		t.Errorf("Genres len = %d, want 2", len(a.Genres))
	}
	if len(a.Images) != 1 {
		t.Errorf("Images len = %d, want 1", len(a.Images))
	}
}

// ─── Null fields / missing optional fields ───────────────────────────

func TestUnmarshalTrackMissingOptionalFields(t *testing.T) {
	// Minimal track JSON — only required fields present.
	data := []byte(`{
		"album": {
			"album_type": "single",
			"total_tracks": 1,
			"external_urls": { "spotify": "" },
			"href": "",
			"id": "aid",
			"images": [],
			"name": "Minimal Album",
			"release_date": "2024-01-01",
			"release_date_precision": "day",
			"type": "album",
			"uri": "",
			"artists": []
		},
		"artists": [],
		"disc_number": 1,
		"duration_ms": 1000,
		"explicit": false,
		"external_ids": {},
		"external_urls": { "spotify": "" },
		"href": "",
		"id": "tid",
		"name": "Minimal Track",
		"popularity": 0,
		"preview_url": null,
		"track_number": 1,
		"type": "track",
		"uri": "",
		"is_local": false
	}`)

	var track Track
	if err := json.Unmarshal(data, &track); err != nil {
		t.Fatalf("unmarshal minimal Track: %v", err)
	}

	if track.ID != "tid" {
		t.Errorf("ID = %q", track.ID)
	}
	if track.PreviewURL != nil {
		t.Error("PreviewURL should be nil")
	}
	if track.IsPlayable != nil {
		t.Error("IsPlayable should be nil when omitted")
	}
	if track.Restrictions != nil {
		t.Error("Restrictions should be nil when omitted")
	}
}

// ─── SimplifiedTrack with optional fields omitted ────────────────────

func TestUnmarshalSimplifiedTrackMinimal(t *testing.T) {
	data := []byte(`{
		"artists": [],
		"disc_number": 1,
		"duration_ms": 1000,
		"explicit": false,
		"external_urls": { "spotify": "" },
		"href": "",
		"id": "stid",
		"name": "Minimal Simplified",
		"preview_url": null,
		"track_number": 1,
		"type": "track",
		"uri": "",
		"is_local": false
	}`)

	var st SimplifiedTrack
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("unmarshal minimal SimplifiedTrack: %v", err)
	}

	if st.ID != "stid" {
		t.Errorf("ID = %q", st.ID)
	}
	if st.IsPlayable != nil {
		t.Error("IsPlayable should be nil when omitted")
	}
	if st.Restrictions != nil {
		t.Error("Restrictions should be nil when omitted")
	}
	if st.PreviewURL != nil {
		t.Error("PreviewURL should be nil for null JSON")
	}
}

// ─── PlaylistTrack with null added_at and added_by ───────────────────

func TestUnmarshalPlaylistTrackNullFields(t *testing.T) {
	data := []byte(`{
		"added_at": null,
		"added_by": null,
		"is_local": false,
		"track": {
			"album": {
				"album_type": "album",
				"total_tracks": 1,
				"external_urls": { "spotify": "" },
				"href": "",
				"id": "a",
				"images": [],
				"name": "A",
				"release_date": "2024",
				"release_date_precision": "year",
				"type": "album",
				"uri": "",
				"artists": []
			},
			"artists": [],
			"disc_number": 1,
			"duration_ms": 1000,
			"explicit": false,
			"external_ids": {},
			"external_urls": { "spotify": "" },
			"href": "",
			"id": "t",
			"name": "T",
			"popularity": 0,
			"preview_url": null,
			"track_number": 1,
			"type": "track",
			"uri": "",
			"is_local": false
		}
	}`)

	var pt PlaylistTrack
	if err := json.Unmarshal(data, &pt); err != nil {
		t.Fatalf("unmarshal PlaylistTrack with null fields: %v", err)
	}

	if pt.AddedAt != nil {
		t.Error("AddedAt should be nil for null JSON")
	}
	if pt.AddedBy != nil {
		t.Error("AddedBy should be nil for null JSON")
	}
	if pt.IsLocal {
		t.Error("IsLocal should be false")
	}
	if pt.Track == nil {
		t.Fatal("Track should not be nil")
	}
	if pt.Track.Name != "T" {
		t.Errorf("Track.Name = %q", pt.Track.Name)
	}
}

// ─── Playlist description null (not set) ─────────────────────────────

func TestUnmarshalPlaylistNullDescription(t *testing.T) {
	data := []byte(`{
		"collaborative": false,
		"description": null,
		"external_urls": { "spotify": "https://open.spotify.com/playlist/p1" },
		"followers": { "href": null, "total": 0 },
		"href": "https://api.spotify.com/v1/playlists/p1",
		"id": "p1",
		"images": [],
		"name": "Untitled Playlist",
		"owner": {
			"external_urls": { "spotify": "" },
			"href": "",
			"id": "u1",
			"type": "user",
			"uri": "",
			"display_name": null
		},
		"public": null,
		"snapshot_id": "snap1",
		"tracks": {
			"href": "",
			"limit": 0,
			"next": null,
			"offset": 0,
			"previous": null,
			"total": 0,
			"items": []
		},
		"type": "playlist",
		"uri": "spotify:playlist:p1"
	}`)

	var pl Playlist
	if err := json.Unmarshal(data, &pl); err != nil {
		t.Fatalf("unmarshal Playlist with null description: %v", err)
	}

	if pl.ID != "p1" {
		t.Errorf("ID = %q", pl.ID)
	}
	if pl.Description != nil {
		t.Error("Description should be nil for null JSON")
	}
	if pl.Public != nil {
		t.Error("Public should be nil for null JSON")
	}
	if pl.Owner.DisplayName != nil {
		t.Error("Owner.DisplayName should be nil for null JSON")
	}
	if pl.Followers.Total != 0 {
		t.Errorf("Followers.Total = %d, want 0", pl.Followers.Total)
	}
}

// ─── Image with null dimensions ──────────────────────────────────────

func TestUnmarshalImageNullDimensions(t *testing.T) {
	data := []byte(`{ "url": "https://i.scdn.co/image/img1", "height": null, "width": null }`)

	var img Image
	if err := json.Unmarshal(data, &img); err != nil {
		t.Fatalf("unmarshal Image with null dims: %v", err)
	}

	if img.URL != "https://i.scdn.co/image/img1" {
		t.Errorf("URL = %q", img.URL)
	}
	if img.Height != nil {
		t.Error("Height should be nil for null JSON")
	}
	if img.Width != nil {
		t.Error("Width should be nil for null JSON")
	}
}

// ─── Paging with empty items array ───────────────────────────────────

func TestUnmarshalPagingEmptyItems(t *testing.T) {
	data := []byte(`{
		"href": "",
		"limit": 50,
		"next": null,
		"offset": 0,
		"previous": null,
		"total": 0,
		"items": []
	}`)

	var page Paging[SimplifiedPlaylist]
	if err := json.Unmarshal(data, &page); err != nil {
		t.Fatalf("unmarshal Paging empty items: %v", err)
	}

	if page.Total != 0 {
		t.Errorf("Total = %d, want 0", page.Total)
	}
	if len(page.Items) != 0 {
		t.Errorf("Items len = %d, want 0", len(page.Items))
	}
}

// ─── SimplifiedAlbum in track context (no restrictions) ──────────────

func TestUnmarshalSimplifiedAlbumNoRestrictions(t *testing.T) {
	data := []byte(`{
		"album_type": "album",
		"total_tracks": 14,
		"external_urls": { "spotify": "https://open.spotify.com/album/a1" },
		"href": "https://api.spotify.com/v1/albums/a1",
		"id": "a1",
		"images": [],
		"name": "No Restrictions Album",
		"release_date": "2023-01-01",
		"release_date_precision": "day",
		"type": "album",
		"uri": "spotify:album:a1",
		"artists": [
			{
				"external_urls": { "spotify": "https://open.spotify.com/artist/ar1" },
				"href": "https://api.spotify.com/v1/artists/ar1",
				"id": "ar1",
				"name": "Artist One",
				"type": "artist",
				"uri": "spotify:artist:ar1"
			}
		]
	}`)

	var sa SimplifiedAlbum
	if err := json.Unmarshal(data, &sa); err != nil {
		t.Fatalf("unmarshal SimplifiedAlbum without restrictions: %v", err)
	}

	if sa.ID != "a1" {
		t.Errorf("ID = %q", sa.ID)
	}
	if sa.Name != "No Restrictions Album" {
		t.Errorf("Name = %q", sa.Name)
	}
	if sa.Restrictions != nil {
		t.Error("Restrictions should be nil when omitted")
	}
	if len(sa.Artists) != 1 {
		t.Errorf("Artists len = %d, want 1", len(sa.Artists))
	}
	if sa.Artists[0].Name != "Artist One" {
		t.Errorf("Artists[0].Name = %q", sa.Artists[0].Name)
	}
}
