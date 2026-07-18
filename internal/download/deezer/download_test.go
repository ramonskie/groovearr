package deezer

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/ramonskie/groovearr/internal/config"
)

func TestGetUserPlaylists_Integration(t *testing.T) {
	if os.Getenv("DEEZER_TEST") != "1" {
		t.Skip("set DEEZER_TEST=1 to run integration test")
	}

	data, err := os.ReadFile("../../../config.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	json.Unmarshal(data, &cfg)

	client := NewDownloadClient(cfg.Deezer, "/tmp")
	if err := client.CheckConnection(context.Background()); err != nil {
		t.Fatalf("auth failed: %v", err)
	}
	t.Logf("user ID: %d", client.UserID())

	// Direct call to inspect response.
	results, err := client.gwCall(context.Background(), "deezer.pageProfile", map[string]any{
		"USER_ID": client.UserID(),
		"tab":     "playlists",
	})
	if err != nil {
		t.Fatalf("gwCall: %v", err)
	}
	t.Logf("results keys: %v", mapKeys(results))

	if tab, ok := results["TAB"].(map[string]any); ok {
		t.Logf("TAB keys: %v", mapKeys(tab))
		if pl, ok := tab["playlists"].(map[string]any); ok {
			t.Logf("playlists keys: %v", mapKeys(pl))
			t.Logf("playlists type: %T", pl["data"])
			if data, ok := pl["data"].([]any); ok {
				t.Logf("data length: %d", len(data))
				if len(data) > 0 {
					raw, _ := json.MarshalIndent(data[0], "", "  ")
					t.Logf("first item: %s", string(raw))
				}
			}
		}
	}

	playlists, err := client.GetUserPlaylists(context.Background())
	if err != nil {
		t.Fatalf("GetUserPlaylists: %v", err)
	}
	t.Logf("found %d playlists", len(playlists))
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m { keys = append(keys, k) }
	return keys
}
