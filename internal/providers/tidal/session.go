// Package tidal implements the Tidal music streaming plugin.
//
// This file ports the upstream Tidal client's session retrieval (session.go)
// to the native plugin client.
//
// Ported from the binozo Tidal client library v0.1.0 (Apache-2.0).
// The upstream client does NO decryption and NO urlPost/signing — plain GET works.
package tidal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Session represents an active Tidal API session.
type Session struct {
	SessionID   string `json:"sessionId"`
	UserID      int    `json:"userId"`
	CountryCode string `json:"countryCode"`
	ChannelID   int    `json:"channelId"`
	PartnerID   int    `json:"partnerId"`
	Client      struct {
		ID                       int     `json:"id"`
		Name                     string  `json:"name"`
		AuthorizedForOffline     bool    `json:"authorizedForOffline"`
		AuthorizedForOfflineDate *string `json:"authorizedForOfflineDate"`
	} `json:"client"`
}

// GetSession retrieves the current session information from the Tidal API.
// The returned Session contains the user's country code and user ID, which
// the plugin uses to validate connectivity and resolve catalog availability.
func (s *streamClient) GetSession(ctx context.Context) (Session, error) {
	data, err := s.api.doRequest(ctx, http.MethodGet, s.api.v1BaseURL+"/sessions", nil)
	if err != nil {
		return Session{}, fmt.Errorf("tidal get session: %w", err)
	}
	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return Session{}, fmt.Errorf("tidal get session unmarshal: %w", err)
	}
	return session, nil
}
