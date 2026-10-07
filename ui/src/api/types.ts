// ─── Health ────────────────────────────────────────────────────────

export interface HealthResponse {
  status: "ok";
}

// ─── Identity ──────────────────────────────────────────────────────

/** Caller identity from GET /api/me. Never contains the API key. */
export interface MeResponse {
  username: string;
  role: string;
  via_api_key: boolean;
}

// ─── Config ────────────────────────────────────────────────────────

export interface SoulseekConfig {
  slskd_url: string;
  api_key: string;
  search_timeout: number;
  min_upload_speed: number;
}

export interface DeezerConfig {
  arl: string;
  quality: "flac" | "mp3_320" | "mp3_128";
  allow_fallback: boolean;
  access_token: string;
}

export interface SpotifyConfig {
  mode: "free" | "dev";
  client_id: string;
  client_secret: string;
  redirect_uri: string;
}

export interface LibraryConfig {
  download_path: string;
  library_path: string;
  folder_template: string;
  playlist_path: string;
  playlist_template: string;
  playlist_auto_sync_mins: number | null;
}

export interface AuthConfig {
  method?: string;
  username?: string;
  password?: string;
  /**
   * Masked form of the API key (`"********"` when configured, `""` otherwise).
   * The raw key is never returned by any endpoint — treat this as a secret
   * sentinel, not a usable credential.
   */
  api_key?: string;
  /** Derived by `Config.Mask()`: true when a real API key is configured. */
  has_api_key?: boolean;
  local_bypass_subnets?: string[];
}

export interface LoggingConfig {
  level: string;
  format: string;
  max_size_mb: number;
  max_backups: number;
  max_age_days: number;
  compress: boolean;
  access_log: boolean | null;
  captured_max: number;
}

/** Controls the artist-tracking refresh loop (config.TrackingConfig). */
export interface TrackingConfig {
  /** Minutes between tracked-artist refresh passes. null/0 = disabled. */
  refresh_mins: number | null;
  /** Queue monitored wanted albums automatically after each refresh. */
  auto_search_missing?: boolean | null;
}

/** One log line as sent by GET /api/logs and streamed as "log_line" SSE events.
 * raw is the untouched line exactly as it appears in the log file. */
export interface LogEntry {
  time: string;
  level: string;
  raw: string;
}

/** Snapshot response for GET /api/logs. */
export interface LogsResponse {
  entries: LogEntry[];
  path: string;
  level: string;
}

export interface Config {
  sources: Record<string, Record<string, unknown>>;
  library: LibraryConfig;
  auth: AuthConfig;
  metadata_order?: string[];
  download_order?: string[];
  album_sources?: string[];
  download_client?: string;
  logging?: LoggingConfig;
  tracking?: TrackingConfig;
  setup_completed?: boolean;
}

/** Partial config payload for PUT /api/config — all fields optional. */
export interface ConfigUpdatePayload {
  sources?: Record<string, Record<string, unknown>>;
  library?: Partial<LibraryConfig>;
  auth?: Partial<AuthConfig>;
  logging?: Partial<LoggingConfig>;
  /** Pointer-merged server-side: omit a field to preserve it, send 0 to disable. */
  tracking?: Partial<TrackingConfig>;
  metadata_order?: string[];
  download_order?: string[];
  album_sources?: string[];
  download_client?: string;
  setup_completed?: boolean;
}

export interface UpdateConfigResponse {
  status: "saved";
}

export interface ConfigValidationError {
  error: string;
  errors: string[];
}

// ─── Users (admin) ─────────────────────────────────────────────────

/** Account role (user.Role: "admin" | "user"). */
export type UserRole = "admin" | "user";

/**
 * One account as returned by GET/POST/PATCH /api/users.
 * Mirrors the backend `userResponse` DTO exactly. The bcrypt hash is never
 * exposed — it does not exist on the wire shape.
 */
export interface UserRecord {
  id: number;
  username: string;
  role: UserRole;
  disabled: boolean;
  created_at: string;
  updated_at: string;
}

/** Payload for POST /api/users. */
export interface CreateUserRequest {
  username: string;
  password: string;
  role: UserRole;
}

/** Payload for PATCH /api/users/{id} — at least one field required. */
export interface UpdateUserRequest {
  role?: UserRole;
  disabled?: boolean;
  password?: string;
}

/** Response for DELETE /api/users/{id}. */
export interface DeleteUserResponse {
  status: "deleted";
}

// ─── Quality Profiles ──────────────────────────────────────────────

export interface AudioQuality {
  format: string;
  bitrate?: number;
  sample_rate?: number;
  bit_depth?: number;
}

export interface QualityTarget {
  label: string;
  format?: string;
  min_bitrate?: number;
  min_sample_rate?: number;
  min_bit_depth?: number;
}

export type UpgradePolicy = "acceptable" | "until_cutoff" | "until_top";
export type SearchMode = "priority" | "best_quality";

export interface QualityProfile {
  id: number;
  name: string;
  description: string;
  ranked_targets: QualityTarget[];
  fallback_enabled: boolean;
  search_mode: SearchMode;
  rank_candidates_by_quality: boolean;
  upgrade_policy: UpgradePolicy;
  upgrade_cutoff_index: number;
  replace_lower_quality: boolean;
  is_default: boolean;
  created_at: string;
  updated_at: string;
}

/** Payload for POST /api/quality-profiles */
export interface QualityProfileCreatePayload {
  name: string;
  description?: string;
  ranked_targets: QualityTarget[];
  fallback_enabled?: boolean;
  search_mode?: SearchMode;
  rank_candidates_by_quality?: boolean;
  upgrade_policy?: UpgradePolicy;
  upgrade_cutoff_index?: number;
  replace_lower_quality?: boolean;
}

/** Payload for PUT /api/quality-profiles/{id} */
export interface QualityProfileUpdatePayload {
  name?: string;
  description?: string;
  ranked_targets?: QualityTarget[];
  fallback_enabled?: boolean;
  search_mode?: SearchMode;
  rank_candidates_by_quality?: boolean;
  upgrade_policy?: UpgradePolicy;
  upgrade_cutoff_index?: number;
  replace_lower_quality?: boolean;
}

// ─── Sources ───────────────────────────────────────────────────────

export type SourceStatus = "not_configured" | "configured" | "connected";

// ─── Plugin Config Schema (manifest-based UI) ────────────────────

export interface FieldOption {
  value: string;
  label: string;
}

export interface FieldDependsOn {
  field: string;
  value: string;
}

export interface FieldValidation {
  format?: string;  // "url", "email"
  min?: number;
  max?: number;
  pattern?: string; // regex
}

export interface ConfigField {
  name: string;
  type: "text" | "password" | "select" | "number" | "toggle";
  label: string;
  hint?: string;
  required: boolean;
  placeholder?: string;
  default?: string;
  options?: FieldOption[];
  depends_on?: FieldDependsOn;
  secret?: boolean;
  validation?: FieldValidation;
}

export interface OAuthConfig {
  enabled: boolean;
  connect_label: string;
  connect_url: string;
  depends_on?: FieldDependsOn;
}

export interface ImportPattern {
  pattern: string;
  label: string;
  is_fallback?: boolean;
}

export interface UISlots {
  playlist_browser: boolean;
  import_url_patterns?: ImportPattern[];
}

export interface SourceInfo {
  name: string;
  display_name: string;
  configured: boolean;
  /** Whether the plugin is enabled (user toggle). Disabled plugins are hidden from selectors. */
  enabled: boolean;
  status: SourceStatus;
  /** Per-capability status: {"download": "connected", "metadata": "connected"} */
  capabilities?: Record<string, SourceStatus>;
  /** Per-capability access model: {"metadata": "public"} = works without provider credentials */
  capability_access?: Record<string, "public" | "account">;
  /** Provider icon identifier (maps to Lucide icon in providerIcons.ts) */
  icon?: string;
  /** Configuration fields for rendering the settings card */
  config_schema?: ConfigField[];
  /** OAuth configuration (shown as connect button if enabled) */
  oauth?: OAuthConfig;
  /** Optional UI feature flags */
  ui_slots?: UISlots;
  /** True when the provider is part of the torrent pipeline (e.g. Prowlarr + qBittorrent) */
  torrent?: boolean;
}

export interface TestConnectionResponse {
  status: string;
  error?: string;
}

// ─── Search ────────────────────────────────────────────────────────

export interface SearchRequest {
  query: string;
  source?: string;
}

/** Base fields shared by search result types. */
export interface SearchResult {
  [key: string]: unknown;
  username: string;
  filename: string;
  size: number;
  bitrate?: number;
  duration?: number;
  quality: string;
  audio_quality?: AudioQuality;
  free_upload_slots: number;
  upload_speed: number;
  queue_length: number;
}

export interface TrackResult extends SearchResult {
  artist?: string;
  title?: string;
  album?: string;
  track_number?: number;
  cover_url?: string;
}

export interface AlbumResult {
  [key: string]: unknown;
  username: string;
  album_path: string;
  album_title: string;
  artist?: string;
  track_count: number;
  total_size: number;
  tracks: TrackResult[];
  dominant_quality: string;
  year?: string;
  free_upload_slots: number;
  upload_speed: number;
  queue_length: number;
}

export interface SearchResponse {
  tracks: TrackResult[];
  albums: AlbumResult[];
}

// ─── Discovery (metadata-first browse) ────────────────────────────

export interface DiscoveryProvider {
  name: string;
  display_name: string;
}

export interface ArtistSummary {
  provider_id: string;
  provider_name: string;
  name: string;
  image_url?: string;
  genres?: string[];
}

export interface DiscoveryAlbum {
  provider_id: string;
  provider_name: string;
  artist_name: string;
  title: string;
  year?: number;
  cover_url?: string;
  track_count: number;
  type: string; // "album", "single", "compilation", "ep"
}

export interface DiscoveryTrack {
  provider_id: string;
  artist_name: string;
  album_title: string;
  title: string;
  track_number: number;
  disc_number: number;
  duration_ms: number;
  isrc?: string;
}

export interface DiscoverySearchResponse {
  artists?: ArtistSummary[];
  albums?: DiscoveryAlbum[];
}

export interface ArtistOverview {
  artist: ArtistSummary;
  top_tracks: DiscoveryTrack[];
  discography: Record<string, number>;
}

export interface DiscoveryAlbumDownloadResponse {
  mode: "album" | "track";
  /** Album mode: single download record ID. */
  download_id?: string;
  /** Album mode: resolved artist name. */
  artist?: string;
  /** Album mode: resolved album name. */
  album?: string;
  /** Track mode: number of tracks queued. */
  queued?: number;
  /** Track mode: total tracks on the album. */
  total?: number;
  /** Track mode: per-track error messages. */
  errors?: string[];
}

export interface DiscoveryTrackEntry {
  title: string;
  track_number: number;
  duration_ms: number;
  downloaded: boolean;
  library_track_id?: number;
  file_path?: string;
  file_size?: number;
  bitrate?: number;
  format?: string;
}

export interface AlbumDiscoveryResponse {
  provider?: string;
  provider_album_id?: string;
  tracks: DiscoveryTrackEntry[];
}

// ─── Downloads ─────────────────────────────────────────────────────

export type DownloadState =
  | "queued"
  | "downloading"
  | "importPending"
  | "importing"
  | "imported"
  | "failedPending"
  | "failed"
  | "ignored";

export interface DownloadRecord {
  id: string;
  source_name: string;
  filename: string;
  display_name: string;
  state: DownloadState;
  progress: number;
  size: number;
  transferred: number;
  speed: number;
  file_path?: string;
  error?: string;
  track_id?: string;
  cover_url?: string;
  playlist_id?: string;
  library_track_id?: number;
  artist?: string;
  album?: string;
  title?: string;
  track_number?: number;
  disc_number?: number;
  year?: number;
  bitrate?: number;
  format?: string;
  retry_count?: number;
  retry_after?: string;
}

/** SSE event type names broadcast from the backend via GET /api/events. */
export type DownloadEventType =
  | "download_queued"
  | "download_stateChanged"
  | "download_progress"
  | "download_completed"
  | "download_failed"
  | "import_completed"
  | "heartbeat";

/** Parsed SSE event payload matching the backend SSEEvent shape. */
export interface DownloadEvent {
  id: string;
  type: DownloadEventType;
  data: DownloadRecord;
  timestamp: string;
}

export interface DownloadRequest {
  source: string;
  username: string;
  filename: string;
  size: number;
  bitrate?: number;
  quality?: string;
}

export interface DownloadResponse {
  download_id: string;
}

export interface DownloadBestRequest {
  title: string;
  artist?: string;
  album?: string;
  duration?: number;
  exclude_source?: string;
}

export interface DownloadBestResponse {
  download_id: string;
  source: string;
  confidence: number;
}

export interface CancelResponse {
  status: "cancelled";
}

// ─── Library ───────────────────────────────────────────────────────

export interface Track {
  id: number;
  album_id: number;
  artist_id: number;
  title: string;
  track_number?: number;
  disc_number?: number;
  duration: number;
  file_path?: string;
  bitrate?: number;
  file_size?: number;
  created_at: string;
  updated_at: string;
  external_ids?: Record<string, string>;
  acoustid?: string;
  isrc?: string;
  /**
   * Who requested the download that imported this track. DB-only attribution
   * (never written to audio tags). Absent/blank for scanned or system imports.
   */
  added_by_user_id?: number;
  added_by_username?: string;
}

export interface Artist {
  id: number;
  name: string;
  genres?: string[];
  summary?: string;
  thumb_url?: string;
  first_album_id?: number;
  created_at: string;
  updated_at: string;
  external_ids?: Record<string, string>;
}

export interface Album {
  id: number;
  artist_id: number;
  title: string;
  year?: number;
  genres?: string[];
  track_count: number;
  duration: number;
  thumb_url?: string;
  album_type?: "album" | "single" | "ep" | "compilation" | "live";
  created_at: string;
  updated_at: string;
  external_ids?: Record<string, string>;
  release_date?: string;
  /**
   * Who requested the download that imported this album. DB-only attribution
   * (never written to audio tags). Absent/blank for scanned or system imports.
   */
  added_by_user_id?: number;
  added_by_username?: string;
}

// ─── Artist tracking ───────────────────────────────────────────────

/** How a tracked artist is monitored for releases (domain.MonitorMode). */
export type MonitorMode = "all" | "future" | "none";

/** Acquisition lifecycle of a discovered tracked album (domain.AlbumStatus). */
export type AlbumStatus = "wanted" | "downloading" | "downloaded" | "ignored";

/** An artist monitored for releases from a provider. Mirrors domain.TrackedArtist. */
export interface TrackedArtist {
  id: number;
  name: string;
  provider_name: string;
  provider_artist_id: string;
  monitored: boolean;
  monitor_mode: MonitorMode;
  /** Set once the artist is matched to a local library entry. */
  library_artist_id?: number;
  auto_refresh: boolean;
  last_refreshed_at?: string;
  created_at: string;
  updated_at: string;
}

/** An album discovered for a tracked artist. Mirrors domain.TrackedAlbum. */
export interface TrackedAlbum {
  id: number;
  tracked_artist_id: number;
  provider_album_id: string;
  provider_name: string;
  title: string;
  /** Omitted when the provider reports no year (Go `omitempty`). */
  year?: number;
  /** Omitted when the provider reports no type (Go `omitempty`). */
  album_type?: string;
  monitored: boolean;
  status: AlbumStatus;
  /** Set once the album is matched to a local library entry. */
  library_album_id?: number;
  first_seen_at: string;
  last_seen_at: string;
}

/** Payload for POST /api/tracking/artists (synchronous 201). */
export interface AddTrackedArtistRequest {
  provider_name: string;
  provider_artist_id: string;
  name: string;
  /** Omit to default to monitored (mode "none" always wins). */
  monitored?: boolean;
  /** Omit to default to "all". */
  monitor_mode?: MonitorMode;
  /** Omit to follow the tracking.refresh_mins setting. */
  auto_refresh?: boolean;
  /**
   * Opt-in: start a search for missing albums once the artist is added.
   * Omit/false to add without searching (Lidarr "Start Search for Missing Albums").
   */
  search_on_add?: boolean;
}

/** Payload for PATCH /api/tracking/artists/{id} — at least one field required. */
export interface UpdateTrackedArtistRequest {
  monitored?: boolean;
  monitor_mode?: MonitorMode;
}

/** Response for GET /api/tracking/artists/{id}: artist plus its albums. */
export interface ArtistWithAlbums {
  artist: TrackedArtist;
  albums: TrackedAlbum[];
}

/** Response for DELETE /api/tracking/artists/{id}. */
export interface DeleteTrackedArtistResponse {
  status: "deleted";
}

/** Payload for PATCH /api/tracking/albums/{id} — at least one field required. */
export interface UpdateTrackedAlbumRequest {
  monitored?: boolean;
  /** Only "wanted" and "ignored" are accepted by the backend. */
  status?: "wanted" | "ignored";
}

/** Response for PATCH /api/tracking/albums/{id}. */
export interface UpdateTrackedAlbumResponse {
  status: "updated";
  id: number;
  /** Present only when the request set `monitored`. */
  monitored?: boolean;
  /** Present only when the request set `status`. */
  album_status?: AlbumStatus;
}

/**
 * Summary of one RefreshArtist pass (tracking.RefreshResult). Job-backed
 * refreshes currently report through the job Manager rather than the HTTP
 * response, so this mirrors the Go JSON shape for consumers of job output.
 */
export interface RefreshResult {
  albums_seen: number;
  newly_wanted: number;
  missing: number;
}

/**
 * Summary of one SearchMissing pass (Go tracking.SearchResult). Same caveat as
 * RefreshResult: surfaced through the job Manager, not the start response.
 */
export interface TrackingSearchResult {
  queued: number;
  skipped: number;
  errors: number;
  /** Wanted albums left for later runs after the per-run batch cap. */
  remaining: number;
}

// ─── Background jobs ────────────────────────────────────────────────

export type JobState = "idle" | "running" | "completed" | "failed" | "cancelled";

export interface Job {
  type: "scan" | "enrich" | "organize" | "duplicates" | "sync";
  state: JobState;
  progress: number; // 0-100
  message?: string;
  done: number;
  total: number;
  started_at?: string;
  finished_at?: string;
  error?: string;
}

export interface StartJobResponse {
  job: Job;
  /** True when this request started the job; false when another was already running. */
  started: boolean;
}

// ─── Organize report ────────────────────────────────────────────────

export interface OrganizeEntry {
  track_id: number;
  from: string;
  to: string;
  reason: string;
}

export interface OrganizeSummary {
  moved: number;
  would_move: number;
  in_place: number;
  skipped: number;
  errors: number;
}

export interface OrganizeReport {
  mode: "dry run" | "repair";
  ran_at: string;
  summary: OrganizeSummary;
  entries: OrganizeEntry[];
  truncated: boolean;
}

// ─── Artist duplicates ──────────────────────────────────────────────

export interface DuplicateArtistEntry {
  id: number;
  name: string;
  track_count: number;
}

export interface DuplicateGroup {
  name: string;
  artists: DuplicateArtistEntry[];
}

export interface ArtistDuplicatesResponse {
  groups: DuplicateGroup[];
}

export interface PaginationParams {
  q?: string;
  offset?: number;
  limit?: number;
}

// ─── Playlists ─────────────────────────────────────────────────────

export interface PlaylistSourceItem {
  name: string;
  display: string;
}

export interface Playlist {
  id: number;
  source: string;
  source_playlist_id: string;
  name: string;
  description?: string;
  track_count: number;
  cover_url?: string;
  owner_name?: string;
  is_public: boolean;
  synced_at?: string;
  auto_sync: boolean;
  sync_mode: string;
  created_at: string;
  updated_at: string;
  /** Derived: another playlist from the same source shares this name. */
  name_conflict?: boolean;
  /** Derived: resolved on-disk folder name (ID-suffixed on conflict). */
  folder_name?: string;
  /**
   * Who imported this playlist. DB-only attribution. Absent/blank for
   * system or unknown imports.
   */
  added_by_user_id?: number;
  added_by_username?: string;
}

/** Per-track download status derived from the download pipeline. */
export type PlaylistTrackDownloadStatus =
  | "linked"
  | "downloading"
  | "queued"
  | "unmatched";

export interface PlaylistTrack {
  playlist_id: number;
  position: number;
  track_id: number | null;
  source_track_id: string;
  title: string;
  artist: string;
  album?: string;
  duration_ms?: number;
  isrc?: string;
  /** Convenience: true when track_id is non-null (linked to library). */
  linked: boolean;
  /** Per-track download status derived from active downloads matching this track. */
  download_status?: PlaylistTrackDownloadStatus;
}

export interface PlaylistDetailResponse {
  playlist: Playlist;
  tracks: PlaylistTrack[];
}

export interface SourcePlaylistItem {
  source_id: string;
  name: string;
  description?: string;
  track_count: number;
  cover_url?: string;
  owner_name?: string;
  imported: boolean;
}

export interface ImportPlaylistRequest {
  source: string;
  playlist_id: string;
  sync_mode?: string;
}

export interface ImportPlaylistResponse {
  playlist: Playlist;
  tracks: PlaylistTrack[];
  linked: number;
  unmatched: number;
}

export interface DownloadMissingResponse {
  queued: number;
}

// Playlist sync runs through the job Manager — same response shape as every
// other background job start.
export type SyncPlaylistResponse = StartJobResponse;

export interface DeletePlaylistResponse {
  status: "deleted";
}

// ─── Generic error shape ───────────────────────────────────────────

export interface ApiError {
  error: string;
}
