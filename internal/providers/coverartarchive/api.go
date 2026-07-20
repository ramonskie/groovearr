package coverartarchive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const baseURL = "https://coverartarchive.org"

// Image represents a single cover art image from the CAA JSON response.
type Image struct {
	ID         string          `json:"id"`
	ImageURL   string          `json:"image"`
	Types      []string        `json:"types"`
	Front      bool            `json:"front"`
	Back       bool            `json:"back"`
	Approved   bool            `json:"approved"`
	Thumbnails map[string]string `json:"thumbnails"` // "250", "500", "1200", "small", "large"
}

// ReleaseImages holds the CAA response for a release.
type ReleaseImages struct {
	Release string  `json:"release"`
	Images  []Image `json:"images"`
}

// apiClient provides access to the Cover Art Archive public API.
// No rate limiting required.
type apiClient struct {
	httpClient *http.Client
}

func newAPIClient() *apiClient {
	return &apiClient{
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// GetReleaseImages fetches all cover art metadata for a release MBID.
func (c *apiClient) GetReleaseImages(ctx context.Context, mbid string) (*ReleaseImages, error) {
	data, err := c.apiGet(ctx, "/release/"+mbid+"/")
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil // not found
	}

	var result ReleaseImages
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ─── Internal HTTP ─────────────────────────────────────────────────────

func (c *apiClient) apiGet(ctx context.Context, path string) (json.RawMessage, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	u = u.JoinPath(path)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("coverartarchive: HTTP %d (read error: %v)", resp.StatusCode, err)
		}
		return nil, fmt.Errorf("coverartarchive: HTTP %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}
