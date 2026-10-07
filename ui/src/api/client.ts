import type {
  HealthResponse,
  MeResponse,
  Config,
  ConfigUpdatePayload,
  UpdateConfigResponse,
  ConfigValidationError,
  LogsResponse,
  SourceInfo,
  TestConnectionResponse,
  SearchRequest,
  SearchResponse,
  DownloadRequest,
  DownloadResponse,
  DownloadBestRequest,
  DownloadBestResponse,
  DownloadRecord,
  CancelResponse,
  Track,
  Artist,
  Album,
  Job,
  StartJobResponse,
  PaginationParams,
  PlaylistSourceItem,
  Playlist,
  PlaylistDetailResponse,
  SourcePlaylistItem,
  ImportPlaylistRequest,
  ImportPlaylistResponse,
  DownloadMissingResponse,
  SyncPlaylistResponse,
  DeletePlaylistResponse,
  DiscoverySearchResponse,
  DiscoveryAlbum,
  DiscoveryTrack,
  DiscoveryAlbumDownloadResponse,
  AlbumDiscoveryResponse,
  ArtistSummary,
  ApiError,
  ArtistOverview,
  QualityProfile,
  QualityProfileCreatePayload,
  QualityProfileUpdatePayload,
  OrganizeReport,
  ArtistDuplicatesResponse,
  TrackedArtist,
  TrackedAlbum,
  AddTrackedArtistRequest,
  UpdateTrackedArtistRequest,
  ArtistWithAlbums,
  DeleteTrackedArtistResponse,
  UpdateTrackedAlbumRequest,
  UpdateTrackedAlbumResponse,
  UserRecord,
  CreateUserRequest,
  UpdateUserRequest,
  DeleteUserResponse,
} from "./types";

// ─── Base fetch wrapper ────────────────────────────────────────────

const BASE_URL = "";

/**
 * Error thrown by request() carrying the HTTP status.
 *
 * Callers need to tell 401 (unauthenticated) apart from 403 (authenticated but
 * forbidden). The auth check must treat a 403 as a normal authenticated error,
 * never as a redirect — doing otherwise caused a login loop (finding C2).
 */
export class ApiRequestError extends Error {
  readonly status: number;

  constructor(message: string, status: number) {
    super(message);
    this.name = "ApiRequestError";
    this.status = status;
  }
}

/**
 * Typed fetch wrapper.  Throws parsed error message on non-ok responses.
 * Auth rides on the same-origin session cookie; on 401 it redirects to login
 * (unless already there).  In dev, Vite proxies /api → localhost:8008; in prod
 * the Go binary serves both.
 */
async function request<T>(
  path: string,
  init?: Omit<RequestInit, "headers"> & { headers?: Record<string, string> },
): Promise<T> {
  const res = await fetch(`${BASE_URL}${path}`, {
    ...init,
    credentials: "same-origin",
    headers: {
      "Content-Type": "application/json",
      ...init?.headers,
    },
  });

  if (!res.ok) {
    if (res.status === 401 && window.location.pathname !== "/login") {
      window.location.href = "/login";
      throw new ApiRequestError("Session expired", 401);
    }
    let message = `HTTP ${res.status} ${res.statusText}`;
    try {
      const body = (await res.json()) as ConfigValidationError | ApiError;
      if (body.error) {
        message = body.error;
      }
    } catch {
      // response is not JSON — keep HTTP status message
    }
    throw new ApiRequestError(message, res.status);
  }

  return res.json() as Promise<T>;
}

/** Like request() but returns a Blob (used for cover art images). */
async function requestBlob(path: string): Promise<Blob> {
  const res = await fetch(`${BASE_URL}${path}`, {
    credentials: "same-origin",
  });
  if (!res.ok) {
    throw new Error(`HTTP ${res.status} ${res.statusText}`);
  }
  return res.blob();
}

/** Build query-string from PaginationParams, skipping undefined values. */
function toQuery(params?: PaginationParams): string {
  if (!params) return "";
  const usp = new URLSearchParams();
  if (params.q !== undefined) usp.set("q", params.q);
  if (params.offset !== undefined) usp.set("offset", String(params.offset));
  if (params.limit !== undefined) usp.set("limit", String(params.limit));
  const qs = usp.toString();
  return qs ? `?${qs}` : "";
}

// ─── Health ────────────────────────────────────────────────────────

export function health(): Promise<HealthResponse> {
  return request<HealthResponse>("/api/health");
}

// ─── Config ────────────────────────────────────────────────────────

export function getConfig(): Promise<Config> {
  return request<Config>("/api/config");
}

// ─── Auth ────────────────────────────────────────────────────────────

export function login(username: string, password: string): Promise<unknown> {
  return request(`/api/login`, {
    method: "POST",
    body: JSON.stringify({ username, password }),
  });
}

export function logout(): Promise<unknown> {
  return request(`/api/logout`, { method: "POST" });
}

/**
 * GET /api/me — resolves the caller's identity (username, role, via_api_key).
 * Used by the auth check; works for every authenticated user, unlike the
 * admin-only GET /api/config. Never returns the API key.
 */
export function getMe(): Promise<MeResponse> {
  return request<MeResponse>("/api/me");
}

export function updateConfig(
  payload: ConfigUpdatePayload,
): Promise<UpdateConfigResponse> {
  return request<UpdateConfigResponse>(`/api/config`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  });
}

export function getLogs(): Promise<LogsResponse> {
  return request<LogsResponse>(`/api/logs`);
}

// ─── Users (admin-only) ──────────────────────────────────────────────
//
// Every /api/users route is wrapped with adminOnly server-side; a non-admin
// receives 403. The SPA additionally hides the management surface (Phase 8.3)
// but the client does not pre-check the role — the server is authoritative.

export function listUsers(): Promise<UserRecord[]> {
  return request<UserRecord[]>("/api/users");
}

/** Creates an account. Server returns 201 with the created record. */
export function createUser(payload: CreateUserRequest): Promise<UserRecord> {
  return request<UserRecord>("/api/users", {
    method: "POST",
    body: JSON.stringify(payload),
  });
}

/**
 * Partial update. Omitted fields are preserved (the backend uses pointer
 * fields), so send only what changed. Returns the updated record.
 */
export function updateUser(
  id: number,
  payload: UpdateUserRequest,
): Promise<UserRecord> {
  return request<UserRecord>(`/api/users/${id}`, {
    method: "PATCH",
    body: JSON.stringify(payload),
  });
}

export function deleteUser(id: number): Promise<DeleteUserResponse> {
  return request<DeleteUserResponse>(`/api/users/${id}`, {
    method: "DELETE",
  });
}

// ─── Setup ───────────────────────────────────────────────────────

export interface SetupStatus {
  needs_setup: boolean;
}

export function getSetupStatus(): Promise<SetupStatus> {
  return request<SetupStatus>("/api/setup/status");
}

// ─── Sources ───────────────────────────────────────────────────────

export function getSources(): Promise<SourceInfo[]> {
  return request<SourceInfo[]>("/api/config/sources");
}

export function testConnection(
  source: string,
): Promise<TestConnectionResponse> {
  return request<TestConnectionResponse>(
    `/api/config/test/${encodeURIComponent(source)}`,
    { method: "POST" },
  );
}

// ─── Search ────────────────────────────────────────────────────────

export function search(payload: SearchRequest): Promise<SearchResponse> {
  return request<SearchResponse>("/api/search", {
    method: "POST",
    body: JSON.stringify(payload),
  });
}

// ─── Downloads ─────────────────────────────────────────────────────

export function download(payload: DownloadRequest): Promise<DownloadResponse> {
  return request<DownloadResponse>("/api/download", {
    method: "POST",
    body: JSON.stringify(payload),
  });
}

export function downloadBest(
  payload: DownloadBestRequest,
): Promise<DownloadBestResponse> {
  return request<DownloadBestResponse>("/api/download/match", {
    method: "POST",
    body: JSON.stringify(payload),
  });
}

export function getDownloads(): Promise<DownloadRecord[]> {
  return request<DownloadRecord[]>("/api/downloads");
}

export function getDownloadsByState(
  state: string,
): Promise<DownloadRecord[]> {
  return request<DownloadRecord[]>(
    `/api/downloads?state=${encodeURIComponent(state)}`,
  );
}

export function cancelDownload(id: string): Promise<CancelResponse> {
  return request<CancelResponse>(
    `/api/downloads/${encodeURIComponent(id)}`,
    { method: "DELETE" },
  );
}

export function retryDownload(id: string): Promise<CancelResponse> {
  return request<CancelResponse>(
    `/api/downloads/${encodeURIComponent(id)}/retry`,
    { method: "POST" },
  );
}

// ─── Library ───────────────────────────────────────────────────────

export function getLibraryTracks(params?: PaginationParams): Promise<Track[]> {
  return request<Track[]>(`/api/library/tracks${toQuery(params)}`);
}

export function getLibraryArtists(params?: PaginationParams): Promise<Artist[]> {
  return request<Artist[]>(`/api/library/artists${toQuery(params)}`);
}

export function getLibraryAlbums(params?: PaginationParams): Promise<Album[]> {
  return request<Album[]>(`/api/library/albums${toQuery(params)}`);
}

// ─── Background jobs ────────────────────────────────────────────────

export function getJob(): Promise<Job | null> {
  return request<Job | null>("/api/jobs");
}

export function startScanJob(): Promise<StartJobResponse> {
  return request<StartJobResponse>("/api/jobs/scan", { method: "POST" });
}

export function startEnrichJob(): Promise<StartJobResponse> {
  return request<StartJobResponse>("/api/jobs/enrich", { method: "POST" });
}

export function startOrganizeJob(dryRun: boolean): Promise<StartJobResponse> {
  return request<StartJobResponse>(`/api/jobs/organize?dryRun=${dryRun}`, {
    method: "POST",
  });
}

export function startDuplicatesJob(): Promise<StartJobResponse> {
  return request<StartJobResponse>("/api/jobs/duplicates", { method: "POST" });
}

export function getOrganizeReport(): Promise<OrganizeReport | null> {
  return request<OrganizeReport | null>("/api/jobs/organize/report");
}

export function getArtistDuplicates(): Promise<ArtistDuplicatesResponse> {
  return request<ArtistDuplicatesResponse>("/api/library/artists/duplicates");
}

export function mergeArtists(
  keepId: number,
  removeId: number,
): Promise<{
  merged: boolean;
  renamed?: boolean;
  canonical_name?: string;
  organize_started?: boolean;
}> {
  return request<{
    merged: boolean;
    renamed?: boolean;
    canonical_name?: string;
    organize_started?: boolean;
  }>(`/api/library/artists/${keepId}/merge`, {
    method: "POST",
    body: JSON.stringify({ remove_id: removeId }),
  });
}

export function cancelJob(): Promise<Job | null> {
  return request<Job | null>("/api/jobs/cancel", { method: "POST" });
}

export function getCoverArt(albumId: number): Promise<Blob> {
  return requestBlob(`/api/covers/${albumId}`);
}

export function getLibraryArtist(artistId: number): Promise<Artist> {
  return request<Artist>(`/api/library/artists/${artistId}`);
}

export function getLibraryArtistAlbums(artistId: number): Promise<Album[]> {
  return request<Album[]>(`/api/library/artists/${artistId}/albums`);
}

export function getLibraryArtistTracks(artistId: number): Promise<Track[]> {
  return request<Track[]>(`/api/library/artists/${artistId}/tracks`);
}

export function getLibraryAlbumDiscovery(
  albumId: number,
): Promise<AlbumDiscoveryResponse> {
  return request<AlbumDiscoveryResponse>(
    `/api/library/albums/${albumId}/discovery`,
  );
}

export function downloadMissingForAlbum(
  albumId: number,
): Promise<DownloadMissingResponse> {
  return request<DownloadMissingResponse>(
    `/api/library/albums/${albumId}/download-missing`,
    { method: "POST" },
  );
}

// ─── Artist tracking ───────────────────────────────────────────────

export function listTrackedArtists(): Promise<TrackedArtist[]> {
  return request<TrackedArtist[]>("/api/tracking/artists");
}

export function getTrackedArtist(artistId: number): Promise<ArtistWithAlbums> {
  return request<ArtistWithAlbums>(`/api/tracking/artists/${artistId}`);
}

/** Starts tracking an artist. Synchronous: returns the created row (201). */
export function addTrackedArtist(
  body: AddTrackedArtistRequest,
): Promise<TrackedArtist> {
  return request<TrackedArtist>("/api/tracking/artists", {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function deleteTrackedArtist(
  artistId: number,
): Promise<DeleteTrackedArtistResponse> {
  return request<DeleteTrackedArtistResponse>(`/api/tracking/artists/${artistId}`, {
    method: "DELETE",
  });
}

export function setArtistMonitor(
  artistId: number,
  body: UpdateTrackedArtistRequest,
): Promise<TrackedArtist> {
  return request<TrackedArtist>(`/api/tracking/artists/${artistId}`, {
    method: "PATCH",
    body: JSON.stringify(body),
  });
}

export function listTrackedAlbums(artistId: number): Promise<TrackedAlbum[]> {
  return request<TrackedAlbum[]>(`/api/tracking/artists/${artistId}/albums`);
}

export function setAlbumMonitored(
  albumId: number,
  monitored: boolean,
): Promise<UpdateTrackedAlbumResponse> {
  return request<UpdateTrackedAlbumResponse>(`/api/tracking/albums/${albumId}`, {
    method: "PATCH",
    body: JSON.stringify({ monitored }),
  });
}

export function setAlbumStatus(
  albumId: number,
  status: "wanted" | "ignored",
): Promise<UpdateTrackedAlbumResponse> {
  const body: UpdateTrackedAlbumRequest = { status };
  return request<UpdateTrackedAlbumResponse>(`/api/tracking/albums/${albumId}`, {
    method: "PATCH",
    body: JSON.stringify(body),
  });
}

export function listAllWanted(): Promise<TrackedAlbum[]> {
  return request<TrackedAlbum[]>("/api/tracking/wanted");
}

// Job-backed actions: provider I/O runs through the job Manager, so these
// return the standard {job, started} start response (StartJobResponse).

export function refreshTrackedArtist(
  artistId: number,
): Promise<StartJobResponse> {
  return request<StartJobResponse>(`/api/tracking/artists/${artistId}/refresh`, {
    method: "POST",
  });
}

export function searchMissingArtist(
  artistId: number,
): Promise<StartJobResponse> {
  return request<StartJobResponse>(
    `/api/tracking/artists/${artistId}/search-missing`,
    { method: "POST" },
  );
}

export function refreshAllTracked(): Promise<StartJobResponse> {
  return request<StartJobResponse>("/api/tracking/refresh", { method: "POST" });
}

// ─── Playlists ─────────────────────────────────────────────────────

export function getPlaylistSources(): Promise<PlaylistSourceItem[]> {
  return request<PlaylistSourceItem[]>("/api/playlists/sources");
}

export function browsePlaylistSource(
  source: string,
): Promise<SourcePlaylistItem[]> {
  return request<SourcePlaylistItem[]>(
    `/api/playlists/sources/${encodeURIComponent(source)}`,
  );
}

export function getPlaylists(): Promise<Playlist[]> {
  return request<Playlist[]>("/api/playlists");
}

export function getPlaylist(id: number): Promise<PlaylistDetailResponse> {
  return request<PlaylistDetailResponse>(`/api/playlists/${id}`);
}

export function importPlaylist(
  payload: ImportPlaylistRequest,
): Promise<ImportPlaylistResponse> {
  return request<ImportPlaylistResponse>("/api/playlists/import", {
    method: "POST",
    body: JSON.stringify(payload),
  });
}

export function downloadMissing(
  playlistId: number,
): Promise<DownloadMissingResponse> {
  return request<DownloadMissingResponse>(
    `/api/playlists/${playlistId}/download-missing`,
    { method: "POST" },
  );
}

export function syncPlaylist(
  playlistId: number,
): Promise<SyncPlaylistResponse> {
  return request<SyncPlaylistResponse>(
    `/api/playlists/${playlistId}/sync`,
    { method: "POST" },
  );
}

export function deletePlaylist(
  playlistId: number,
): Promise<DeletePlaylistResponse> {
  return request<DeletePlaylistResponse>(
    `/api/playlists/${playlistId}`,
    { method: "DELETE" },
  );
}

export function updatePlaylist(
  playlistId: number,
  patch: { auto_sync?: boolean; sync_mode?: string },
): Promise<Playlist> {
  return request<Playlist>(`/api/playlists/${playlistId}`, {
    method: "PATCH",
    body: JSON.stringify(patch),
  });
}

// ─── Discovery ────────────────────────────────────────────────────

export function getDiscoveryProviders() {
  return request<Array<{ name: string; display_name: string }>>(
    "/api/discover/providers",
  );
}

export function discoverySearch(query: string, type?: string) {
  const params = new URLSearchParams({ q: query });
  if (type) params.set("type", type);
  return request<DiscoverySearchResponse>(`/api/discover/search?${params}`);
}

/** Resolve a library artist name to a discovery provider's ArtistSummary. */
export function resolveDiscoveryArtist(name: string) {
  return request<ArtistSummary>(
    `/api/discover/artists/resolve?q=${encodeURIComponent(name)}`,
  );
}

/** Fetch artist overview: top tracks + discography stats from discovery provider. */
export function getArtistOverview(name: string) {
  return request<ArtistOverview>(
    `/api/discover/artists/overview?q=${encodeURIComponent(name)}`,
  );
}

export function getArtistAlbums(artistId: string, provider?: string) {
  const qs = provider ? `?provider=${encodeURIComponent(provider)}` : "";
  return request<DiscoveryAlbum[]>(
    `/api/discover/artists/${encodeURIComponent(artistId)}/albums${qs}`,
  );
}

export function getAlbumTracks(albumId: string, provider?: string) {
  const qs = provider ? `?provider=${encodeURIComponent(provider)}` : "";
  return request<DiscoveryTrack[]>(
    `/api/discover/albums/${encodeURIComponent(albumId)}/tracks${qs}`,
  );
}

export function downloadAlbum(
  albumId: string,
  artistName?: string,
  albumName?: string,
) {
  return request<DiscoveryAlbumDownloadResponse>(
    `/api/discover/albums/${encodeURIComponent(albumId)}/download`,
    {
      method: "POST",
      body: JSON.stringify({
        artist_name: artistName || "",
        album_name: albumName || "",
      }),
    },
  );
}

// ─── Quality Profiles ──────────────────────────────────────────────

export function getQualityProfiles(): Promise<QualityProfile[]> {
  return request<QualityProfile[]>("/api/quality-profiles");
}

export function getQualityProfile(id: number): Promise<QualityProfile> {
  return request<QualityProfile>(`/api/quality-profiles/${id}`);
}

export function createQualityProfile(
  payload: QualityProfileCreatePayload,
): Promise<QualityProfile> {
  return request<QualityProfile>("/api/quality-profiles", {
    method: "POST",
    body: JSON.stringify(payload),
  });
}

export function updateQualityProfile(
  id: number,
  payload: QualityProfileUpdatePayload,
): Promise<QualityProfile> {
  return request<QualityProfile>(`/api/quality-profiles/${id}`, {
    method: "PUT",
    body: JSON.stringify(payload),
  });
}

export function deleteQualityProfile(id: number): Promise<void> {
  return request<void>(`/api/quality-profiles/${id}`, {
    method: "DELETE",
  });
}

export function setDefaultQualityProfile(id: number): Promise<void> {
  return request<void>(`/api/quality-profiles/${id}/default`, {
    method: "PUT",
  });
}

export function getQualityPresets(): Promise<Record<string, QualityProfile>> {
  return request<Record<string, QualityProfile>>("/api/quality-profiles/presets");
}
