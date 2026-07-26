package plugin

// ConfigField describes a single configuration field for a plugin's settings form.
// It is used by the frontend to render provider-specific settings cards without
// hardcoded per-provider React components.
type ConfigField struct {
	Name        string          `json:"name"`
	Type        string          `json:"type"` // "text", "password", "select", "number"
	Label       string          `json:"label"`
	Hint        string          `json:"hint,omitempty"`
	Required    bool            `json:"required"`
	Placeholder string          `json:"placeholder,omitempty"`
	Default     string          `json:"default,omitempty"`
	Options     []FieldOption   `json:"options,omitempty"`
	DependsOn   *FieldDependsOn `json:"depends_on,omitempty"`
	Secret      bool            `json:"secret,omitempty"`    // mask value in UI (password fields)
	Validation  *FieldValidation `json:"validation,omitempty"`
}

// FieldOption is a select dropdown option.
type FieldOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// FieldDependsOn declares that a field should only be visible when another
// field has a specific value (e.g., Spotify client_id only when mode="dev").
type FieldDependsOn struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

// FieldValidation declares validation rules for a field.
type FieldValidation struct {
	Format  string  `json:"format,omitempty"`  // "url", "email"
	Min     *int    `json:"min,omitempty"`
	Max     *int    `json:"max,omitempty"`
	Pattern string  `json:"pattern,omitempty"` // regex
}

// OAuthInfo describes an OAuth flow for plugins that require user authentication.
// When non-nil, the frontend renders a "Connect {label}" button linking to the
// authorization URL. DependsOn optionally ties visibility to a config field value
// (e.g., Spotify's OAuth only shows when mode="dev").
type OAuthInfo struct {
	Enabled      bool            `json:"enabled"`
	ConnectLabel string          `json:"connect_label"`
	ConnectURL   string          `json:"connect_url"`
	DependsOn    *FieldDependsOn `json:"depends_on,omitempty"`
}

// UISlots declares optional UI features a plugin provides beyond config forms.
type UISlots struct {
	PlaylistBrowser  bool            `json:"playlist_browser"`
	ImportURLPatterns []ImportPattern `json:"import_url_patterns,omitempty"`
}

// ImportPattern describes a URL pattern that the import dialog can parse
// to extract playlist IDs from user-pasted URLs.
type ImportPattern struct {
	Pattern     string `json:"pattern"`               // regex with capture group
	Label       string `json:"label"`                 // human-readable description
	IsFallback  bool   `json:"is_fallback,omitempty"` // try this if no other pattern matches
}

// ConfigSchemaProvider is implemented by plugin factories that want to expose
// their configuration schema to the frontend. Factories that don't implement
// this receive no settings card in the UI.
type ConfigSchemaProvider interface {
	ConfigSchema() []ConfigField
	Icon() string
	OAuthConfig() *OAuthInfo
	UISlots() *UISlots
}
