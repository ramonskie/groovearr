package tidal

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ramonskie/groovearr/internal/playlist"
	"github.com/ramonskie/groovearr/internal/plugin"
)

// ─── PlaylistSource Adapter ─────────────────────────────────────────────

func TestPlaylistSource(t *testing.T) {
	// Create a client from the factory with a valid config so we can access
	// the playlist adapter. The client does not need auth for these adapter tests.
	resources := pluginResources(t)
	raw := jsonRawMessage(`{"access_token":"","country_code":"US","quality":"LOSSLESS"}`)
	client, err := Factory.Create(raw, resources)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	defer client.(*Client).tiddlClient.Close()

	ps := client.(*Client).PlaylistSource()
	if ps == nil {
		t.Fatal("PlaylistSource returned nil")
	}
}

func TestPlaylistSourceName(t *testing.T) {
	resources := pluginResources(t)
	raw := jsonRawMessage(`{"access_token":"","country_code":"US","quality":"LOSSLESS"}`)
	client, err := Factory.Create(raw, resources)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	defer client.(*Client).tiddlClient.Close()

	ps := client.(*Client).PlaylistSource()
	if ps.Name() != "tidal" {
		t.Errorf("Name() = %q, want tidal", ps.Name())
	}
}

func TestPlaylistSourceDisplayName(t *testing.T) {
	resources := pluginResources(t)
	raw := jsonRawMessage(`{"access_token":"","country_code":"US","quality":"LOSSLESS"}`)
	client, err := Factory.Create(raw, resources)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	defer client.(*Client).tiddlClient.Close()

	ps := client.(*Client).PlaylistSource()
	if ps.DisplayName() != "Tidal" {
		t.Errorf("DisplayName() = %q, want Tidal", ps.DisplayName())
	}
}

func TestPlaylistSourceIsConfiguredWithoutToken(t *testing.T) {
	resources := pluginResources(t)
	raw := jsonRawMessage(`{"access_token":"","country_code":"US","quality":"LOSSLESS"}`)
	client, err := Factory.Create(raw, resources)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	defer client.(*Client).tiddlClient.Close()

	ps := client.(*Client).PlaylistSource()
	if ps.IsConfigured() {
		t.Error("IsConfigured should be false when access_token is empty")
	}
}

func TestPlaylistSourceIsConfiguredWithToken(t *testing.T) {
	resources := pluginResources(t)
	raw := jsonRawMessage(`{"access_token":"fake-token","country_code":"US","quality":"LOSSLESS"}`)
	client, err := Factory.Create(raw, resources)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	defer client.(*Client).tiddlClient.Close()

	ps := client.(*Client).PlaylistSource()
	if !ps.IsConfigured() {
		t.Error("IsConfigured should be true when access_token is set")
	}
}

func TestGetUserPlaylistsUnconfigured(t *testing.T) {
	resources := pluginResources(t)
	raw := jsonRawMessage(`{"access_token":"","country_code":"US","quality":"LOSSLESS"}`)
	client, err := Factory.Create(raw, resources)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	defer client.(*Client).tiddlClient.Close()

	ps := client.(*Client).PlaylistSource()
	playlists, err := ps.GetUserPlaylists(context.Background())
	if err != nil {
		t.Errorf("unconfigured GetUserPlaylists should not error: %v", err)
	}
	if playlists != nil {
		t.Errorf("unconfigured GetUserPlaylists should return nil, got %d items", len(playlists))
	}
}

func TestGetPlaylistTracksUnconfigured(t *testing.T) {
	resources := pluginResources(t)
	raw := jsonRawMessage(`{"access_token":"","country_code":"US","quality":"LOSSLESS"}`)
	client, err := Factory.Create(raw, resources)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	defer client.(*Client).tiddlClient.Close()

	ps := client.(*Client).PlaylistSource()
	tracks, name, err := ps.GetPlaylistTracks(context.Background(), "some-uuid")
	if err != nil {
		t.Errorf("unconfigured GetPlaylistTracks should not error: %v", err)
	}
	if tracks != nil {
		t.Errorf("unconfigured GetPlaylistTracks should return nil tracks, got %d", len(tracks))
	}
	if name != "" {
		t.Errorf("unconfigured GetPlaylistTracks should return empty name, got %q", name)
	}
}

// ─── Client playlist methods ────────────────────────────────────────────

func TestClientPlaylistSourceReturnsAdapter(t *testing.T) {
	resources := pluginResources(t)
	raw := jsonRawMessage(`{"access_token":"","country_code":"US","quality":"LOSSLESS"}`)
	client, err := Factory.Create(raw, resources)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	defer client.(*Client).tiddlClient.Close()

	var _ playlist.Source = client.(*Client).PlaylistSource()
}

// ─── Client BasePlugin compliance ───────────────────────────────────────

func TestClientName(t *testing.T) {
	resources := pluginResources(t)
	raw := jsonRawMessage(`{"access_token":"","country_code":"US","quality":"LOSSLESS"}`)
	client, err := Factory.Create(raw, resources)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	defer client.(*Client).tiddlClient.Close()

	if client.Name() != "tidal" {
		t.Errorf("Name() = %q, want tidal", client.Name())
	}
	if client.DisplayName() != "Tidal" {
		t.Errorf("DisplayName() = %q, want Tidal", client.DisplayName())
	}
}

func TestClientIsConfigured(t *testing.T) {
	t.Run("empty token", func(t *testing.T) {
		resources := pluginResources(t)
		raw := jsonRawMessage(`{"access_token":"","country_code":"US"}`)
		client, err := Factory.Create(raw, resources)
		if err != nil {
			t.Fatalf("Create failed: %v", err)
		}
		defer client.(*Client).tiddlClient.Close()
		if client.IsConfigured() {
			t.Error("IsConfigured should be false")
		}
	})
	t.Run("with token", func(t *testing.T) {
		resources := pluginResources(t)
		raw := jsonRawMessage(`{"access_token":"test-token","country_code":"US"}`)
		client, err := Factory.Create(raw, resources)
		if err != nil {
			t.Fatalf("Create failed: %v", err)
		}
		defer client.(*Client).tiddlClient.Close()
		if !client.IsConfigured() {
			t.Error("IsConfigured should be true")
		}
	})
}

func TestClientCapabilityStatus(t *testing.T) {
	resources := pluginResources(t)
	raw := jsonRawMessage(`{"access_token":"","country_code":"US","quality":"LOSSLESS"}`)
	client, err := Factory.Create(raw, resources)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	defer client.(*Client).tiddlClient.Close()

	status := client.CapabilityStatus()
	for _, cap := range []string{"download", "playlist", "discovery", "metadata"} {
		if _, ok := status[cap]; !ok {
			t.Errorf("CapabilityStatus missing key %q", cap)
		}
	}
	// Without token, all should be "not_configured".
	if status["download"] != "not_configured" {
		t.Errorf("download status = %q, want not_configured", status["download"])
	}
}

func TestClientConnected(t *testing.T) {
	resources := pluginResources(t)
	raw := jsonRawMessage(`{"access_token":"","country_code":"US","quality":"LOSSLESS"}`)
	client, err := Factory.Create(raw, resources)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	defer client.(*Client).tiddlClient.Close()

	if client.Connected() {
		t.Error("Connected should be false before CheckConnection succeeds")
	}
}

func TestClientCheckConnectionNoToken(t *testing.T) {
	resources := pluginResources(t)
	raw := jsonRawMessage(`{"access_token":"","country_code":"US","quality":"LOSSLESS"}`)
	client, err := Factory.Create(raw, resources)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	defer client.(*Client).tiddlClient.Close()

	err = client.CheckConnection(context.Background())
	if err == nil {
		t.Error("CheckConnection should fail without access token")
	}
	if client.Connected() {
		t.Error("Connected should remain false after failed check")
	}
}

// ─── Helpers ────────────────────────────────────────────────────────────

func pluginResources(t *testing.T) plugin.PluginResources {
	t.Helper()
	return plugin.PluginResources{
		DownloadPath: t.TempDir(),
	}
}

func jsonRawMessage(s string) json.RawMessage {
	return json.RawMessage(s)
}
