import type {
  HealthResponse,
  Config,
  ConfigUpdatePayload,
  UpdateConfigResponse,
  ConfigValidationError,
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
  ScanStats,
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
  ApiError,
  QualityProfile,
  QualityProfileCreatePayload,
  QualityProfileUpdatePayload,
} from "./types";

// ─── Base fetch wrapper ────────────────────────────────────────────

const BASE_URL = "";

/**
 * Typed fetch wrapper.  Throws parsed error message on non-ok responses.
 * On 401, clears stored state and redirects to login (unless already there).
 * In dev, Vite proxies /api → localhost:8008; in prod the Go binary serves both.
 */
async function request<T>(
  path: string,
  init?: Omit<RequestInit, "headers"> & { headers?: Record<string, string> },
): Promise<T> {
  const apiKey = getStoredApiKey();
  const res = await fetch(`${BASE_URL}${path}`, {
    ...init,
    headers: {
      "Content-Type": "application/json",
      ...(apiKey ? { "X-Api-Key": apiKey } : {}),
      ...init?.headers,
    },
  });

  if (!res.ok) {
    if (res.status === 401 && window.location.pathname !== "/login") {
      try { localStorage.removeItem("groovearr_api_key"); } catch {}
      window.location.href = "/login";
      throw new Error("Session expired");
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
    throw new Error(message);
  }

  return res.json() as Promise<T>;
}

/** Like request() but returns a Blob (used for cover art images). */
async function requestBlob(path: string): Promise<Blob> {
  const apiKey = getStoredApiKey();
  const res = await fetch(`${BASE_URL}${path}`, {
    headers: apiKey ? { "X-Api-Key": apiKey } : {},
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

export function updateConfig(
  payload: ConfigUpdatePayload,
): Promise<UpdateConfigResponse> {
  return request<UpdateConfigResponse>("/api/config", {
    method: "PUT",
    body: JSON.stringify(payload),
  });
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

export function scanLibrary(): Promise<ScanStats> {
  return request<ScanStats>("/api/library/scan", { method: "POST" });
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

export function getArtistAlbums(artistId: string) {
  return request<DiscoveryAlbum[]>(
    `/api/discover/artists/${encodeURIComponent(artistId)}/albums`,
  );
}

export function getAlbumTracks(albumId: string) {
  return request<DiscoveryTrack[]>(
    `/api/discover/albums/${encodeURIComponent(albumId)}/tracks`,
  );
}

export function downloadAlbum(albumId: string) {
  return request<DiscoveryAlbumDownloadResponse>(
    `/api/discover/albums/${encodeURIComponent(albumId)}/download`,
    { method: "POST" },
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

// ─── Auth helpers ───────────────────────────────────────────────────

const API_KEY_KEY = "groovearr_api_key";

function getStoredApiKey(): string | null {
  try {
    return localStorage.getItem(API_KEY_KEY);
  } catch {
    return null;
  }
}
