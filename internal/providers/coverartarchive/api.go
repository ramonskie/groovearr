package coverartarchive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/ramonskie/groovearr/internal/ratelimit"
)

const baseURL = "https://coverartarchive.org"

// caaRate limits outgoing requests to the Cover Art Archive to 5 req/s.
const caaRate = 5

// Image represents a single cover art image from the CAA JSON response.
type Image struct {
	ID         string            `json:"id"`
	ImageURL   string            `json:"image"`
	Types      []string          `json:"types"`
	Front      bool              `json:"front"`
	Back       bool              `json:"back"`
	Approved   bool              `json:"approved"`
	Thumbnails map[string]string `json:"thumbnails"` // "250", "500", "1200", "small", "large"
}

// ReleaseImages holds the CAA response for a release.
type ReleaseImages struct {
	Release string  `json:"release"`
	Images  []Image `json:"images"`
}

// apiClient provides access to the Cover Art Archive public API.
// Rate limited to 5 req/s to be polite to the archive.
type apiClient struct {
	httpClient *http.Client
	log        *slog.Logger
}

func newAPIClient(log *slog.Logger) *apiClient {
	return &apiClient{
		httpClient: &http.Client{
			Timeout:   15 * time.Second,
			Transport: ratelimit.NewRateLimitedTransport(http.DefaultTransport, caaRate),
		},
		log: log,
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
		if c.log != nil {
			c.log.Error("coverartarchive URL parse failed", "error", err, "component", "caa_api")
		}
		return nil, err
	}
	u = u.JoinPath(path)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		if c.log != nil {
			c.log.Error("coverartarchive request creation failed", "error", err, "component", "caa_api")
		}
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if c.log != nil {
			c.log.Error("coverartarchive request failed", "error", err, "component", "caa_api")
		}
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			if c.log != nil {
				c.log.Error("coverartarchive read error body failed", "error", err, "status", resp.StatusCode, "component", "caa_api")
			}
			return nil, fmt.Errorf("coverartarchive: HTTP %d (read error: %v)", resp.StatusCode, err)
		}
		return nil, fmt.Errorf("coverartarchive: HTTP %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		if c.log != nil {
			c.log.Error("coverartarchive read response body failed", "error", err, "component", "caa_api")
		}
		return nil, err
	}
	return json.RawMessage(body), nil
}
