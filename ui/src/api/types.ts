// ─── Health ────────────────────────────────────────────────────────

export interface HealthResponse {
  status: "ok";
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

export interface LibraryConfig {
  download_path: string;
  library_path: string;
  folder_template: string;
  playlist_path: string;
  playlist_template: string;
}

export interface QualityConfig {
  preferred_format: "flac" | "mp3" | "any";
  min_bitrate: number;
}

export interface Config {
  soulseek: SoulseekConfig;
  deezer: DeezerConfig;
  library: LibraryConfig;
  quality: QualityConfig;
}

/** Partial config payload for PUT /api/config — all fields optional. */
export interface ConfigUpdatePayload {
  soulseek?: Partial<SoulseekConfig>;
  deezer?: Partial<DeezerConfig>;
  library?: Partial<LibraryConfig>;
  quality?: Partial<QualityConfig>;
}

export interface UpdateConfigResponse {
  status: "saved";
}

export interface ConfigValidationError {
  error: string;
  errors: string[];
}

// ─── Sources ───────────────────────────────────────────────────────

export type SourceStatus = "not_configured" | "configured" | "connected";

export interface SourceInfo {
  name: string;
  display_name: string;
  configured: boolean;
  status: SourceStatus;
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
}

export interface DownloadResponse {
  download_id: string;
}

export interface DownloadBestRequest {
  title: string;
  artist?: string;
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
}

export interface Artist {
  id: number;
  name: string;
  genres?: string[];
  summary?: string;
  thumb_url?: string;
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
}

export interface ScanStats {
  scanned: number;
  imported: number;
  skipped: number;
  errors: number;
  paths: string[];
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
  created_at: string;
  updated_at: string;
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

export interface SyncPlaylistResponse {
  status: "syncing";
}

export interface DeletePlaylistResponse {
  status: "deleted";
}

// ─── Generic error shape ───────────────────────────────────────────

export interface ApiError {
  error: string;
}
