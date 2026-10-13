import { useMemo } from "react";
import {
  useInfiniteQuery,
  useQuery,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import {
  ApiRequestError,
  getLibraryTracks,
  getLibraryArtists,
  getLibraryAlbums,
  getLibraryAlbum,
  getLibraryArtist,
  getLibraryArtistAlbums,
  getLibraryArtistTracks,
  getLibraryAlbumDiscovery,
  downloadMissingForAlbum,
  refreshAlbumMetadata,
} from "../api/client";
import type {
  Album,
  Artist,
  AlbumDiscoveryResponse,
  DiscoveryTrackEntry,
} from "../api/types";

// ─── Shared cache config ────────────────────────────────────────────

/** Library data rarely changes (only on scan) — keep fresh for 30m, cache for 24h. */
const libDefaults = {
  staleTime: 30 * 60 * 1000,       // 30m — refetch in background after this
  gcTime: 24 * 60 * 60 * 1000,     // 24h — don't garbage collect
} as const;

// ─── Artist list pagination ─────────────────────────────────────────

const ARTIST_PAGE_SIZE = 100;
// Request one extra item per page so we can detect whether more pages exist.
const ARTIST_FETCH_LIMIT = ARTIST_PAGE_SIZE + 1;

// ─── Per-artist album pagination ────────────────────────────────────

export const ALBUM_PAGE_SIZE = 50;
// Request one extra item per page so we can detect whether more pages exist.
const ALBUM_FETCH_LIMIT = ALBUM_PAGE_SIZE + 1;

/**
 * The infinite query is only enabled once an artist is selected. Extracted as a
 * pure predicate so the null-artist gate is unit-testable without a renderer.
 */
export function isArtistAlbumsEnabled(artistId: number | null): boolean {
  return artistId != null;
}

/**
 * Next-page offset for the per-artist album infinite query, or `undefined` when
 * the last page carried no page+1 sentinel (i.e. no further pages). Mirrors the
 * artist-list strategy: every full page contributes exactly `ALBUM_PAGE_SIZE`
 * visible rows, so the next offset is `pages × page size`.
 */
export function albumNextPageOffset(
  lastPage: Album[] | undefined,
  allPages: Album[][],
): number | undefined {
  if (!lastPage || lastPage.length <= ALBUM_PAGE_SIZE) return undefined;
  return allPages.length * ALBUM_PAGE_SIZE;
}

/** Flatten fetched album pages, dropping the page+1 sentinel from each page. */
export function flattenAlbumPages(pages: Album[][] | undefined): Album[] {
  const items: Album[] = [];
  for (const page of pages ?? []) {
    items.push(...(page ?? []).slice(0, ALBUM_PAGE_SIZE));
  }
  return items;
}

// ─── Queries ────────────────────────────────────────────────────────

export function useLibraryTracks(q?: string) {
  return useQuery({
    queryKey: ["library", "tracks", q] as const,
    queryFn: () => getLibraryTracks({ q }),
    ...libDefaults,
  });
}

export function useLibraryArtists(q?: string) {
  const query = useInfiniteQuery({
    queryKey: ["library", "artists", q] as const,
    queryFn: ({ pageParam }) =>
      getLibraryArtists({ q, offset: pageParam as number, limit: ARTIST_FETCH_LIMIT }),
    initialPageParam: 0,
    getNextPageParam: (lastPage, allPages) => {
      if (!lastPage || lastPage.length <= ARTIST_PAGE_SIZE) return undefined;
      return allPages.length * ARTIST_PAGE_SIZE;
    },
    ...libDefaults,
  });

  const artists = useMemo(() => {
    const items: Artist[] = [];
    for (const page of query.data?.pages ?? []) {
      items.push(...(page ?? []).slice(0, ARTIST_PAGE_SIZE));
    }
    return items;
  }, [query.data]);

  return { ...query, artists };
}

export function useLibraryAlbums(q?: string) {
  return useQuery({
    queryKey: ["library", "albums", q] as const,
    queryFn: () => getLibraryAlbums({ q }),
    ...libDefaults,
  });
}

export function useLibraryArtist(artistId: number | null) {
  return useQuery({
    queryKey: ["library", "artist", artistId] as const,
    queryFn: () => getLibraryArtist(artistId!),
    enabled: artistId != null,
    ...libDefaults,
  });
}

/**
 * Canonical query key for one album's library record. It is a prefix of the
 * discovery key (`["library","album",id,"discovery"]`), so invalidating
 * `["library"]` refreshes both while each stays a distinct cache entry.
 */
export function libraryAlbumQueryKey(albumId: number | null) {
  return ["library", "album", albumId] as const;
}

/**
 * True when an error is an HTTP 404 surfaced by `api/client.ts`. `request()`
 * throws an `ApiRequestError` carrying the status; a 404 means the resource
 * genuinely does not exist, so it must not be retried nor conflated with a
 * transient failure. `instanceof` matches the repo convention used by
 * `AuthContext` for the 403 case.
 */
export function isNotFoundError(error: unknown): boolean {
  return error instanceof ApiRequestError && error.status === 404;
}

/**
 * Retry policy for the by-id album query. A 404 is terminal — the album does
 * not exist, so retrying only prolongs the spinner on a bad deep link. Every
 * other (transient) error keeps react-query's default three-attempt backoff.
 * Pure so the policy is unit-testable without a renderer.
 */
export function shouldRetryLibraryAlbum(
  failureCount: number,
  error: unknown,
): boolean {
  return !isNotFoundError(error) && failureCount < 3;
}

/**
 * The single-album query is enabled once an album is selected AND the loaded
 * artist-album grid has not already produced it. Extracted as a pure predicate
 * so the gate is unit-testable without a DOM renderer (this repo's vitest setup
 * has none).
 *
 * `albumInGrid` keeps the click-from-grid path from firing a redundant by-id
 * request: the grid already holds the album, so its record renders directly.
 * The deep-link path (album not on any loaded page) leaves `albumInGrid` false,
 * so the query runs and resolves the album independently of pagination.
 */
export function isLibraryAlbumEnabled(
  albumId: number | null,
  albumInGrid = false,
): boolean {
  return albumId != null && !albumInGrid;
}

/**
 * Resolve one album by ID, independent of the paginated artist-album grid. The
 * library page uses it so a deep link / reload to `?artist=X&album=Y` resolves
 * Y even when it is not on a loaded page of the artist's albums.
 *
 * `albumInGrid` (default false) disables the query when the loaded grid already
 * carries the album, avoiding the redundant request; the grid record renders
 * instead.
 */
export function useLibraryAlbum(albumId: number | null, albumInGrid = false) {
  return useQuery({
    queryKey: libraryAlbumQueryKey(albumId),
    queryFn: () => getLibraryAlbum(albumId!),
    enabled: isLibraryAlbumEnabled(albumId, albumInGrid),
    retry: shouldRetryLibraryAlbum,
    ...libDefaults,
  });
}

export function useLibraryArtistAlbums(artistId: number | null) {
  const query = useInfiniteQuery({
    queryKey: ["library", "artist", artistId, "albums"] as const,
    queryFn: ({ pageParam }) =>
      getLibraryArtistAlbums(artistId!, {
        offset: pageParam as number,
        limit: ALBUM_FETCH_LIMIT,
      }),
    initialPageParam: 0,
    getNextPageParam: (lastPage, allPages) =>
      albumNextPageOffset(lastPage, allPages),
    enabled: isArtistAlbumsEnabled(artistId),
    ...libDefaults,
  });

  const albums = useMemo(() => flattenAlbumPages(query.data?.pages), [query.data]);

  return { ...query, albums };
}

export function useLibraryArtistTracks(artistId: number | null) {
  return useQuery({
    queryKey: ["library", "artist", artistId, "tracks"] as const,
    queryFn: () => getLibraryArtistTracks(artistId!),
    enabled: artistId != null,
    ...libDefaults,
  });
}

// ─── Album discovery: view state ────────────────────────────────────

/**
 * Album discovery payload plus the additive catalog metadata flags the
 * endpoint returns when the provider tracklist has not been materialized yet
 * (a cold read). Declared in the hook layer so the shared wire types stay
 * untouched; every field is optional so older payloads remain valid.
 *
 * - `metadata_pending` — true while the provider tracklist is unmaterialized.
 *   It stays true for a terminal album (its tracklist never materialized), so
 *   it must not be read as "still working".
 * - `metadata_status` — the catalog refresh status (`pending` | `fresh` |
 *   `stale` | `unmatched` | `failed`). Passed through for display only.
 * - `metadata_terminal` — true once the album is parked in a terminal state
 *   (`unmatched`/`failed`); the UI must show a terminal message, not a spinner.
 */
export interface AlbumDiscoveryView extends AlbumDiscoveryResponse {
  metadata_pending?: boolean;
  metadata_status?: string;
  metadata_terminal?: boolean;
}

/** Coarse view state the album components switch on. */
export type AlbumDiscoveryStatus =
  | "idle" // no album selected
  | "loading" // cold: no data yet, full-screen spinner
  | "error"
  | "warm" // materialized: full provider tracklist
  | "pending" // partial: local fallback shown, provider tracklist materializing
  | "terminal"; // parked: no match / failed, tracklist will not materialize

export interface AlbumDiscoveryViewState {
  status: AlbumDiscoveryStatus;
  /** Provider merged tracklist, or the local fallback while pending/terminal. */
  tracks: DiscoveryTrackEntry[];
  /**
   * True while the provider tracklist is still unmaterialized. Stays true for
   * a terminal album; use `status === "terminal"` to tell the two apart.
   */
  metadataPending: boolean;
}

/** Canonical query key for one album's discovery payload. */
export function albumDiscoveryQueryKey(albumId: number | null) {
  return ["library", "album", albumId, "discovery"] as const;
}

/**
 * Pure mapping from the react-query result to the warm/cold/pending/terminal
 * view state (AGENTS §4 — the component switches on this, not on raw flags).
 * Extracted so it can be unit-tested without a React renderer.
 *
 * Terminal wins over pending: a parked album still reports
 * `metadata_pending=true` (nothing materialized) but must resolve to
 * `"terminal"` so the UI stops showing a perpetual spinner.
 */
export function albumDiscoveryViewState(input: {
  albumId: number | null;
  isLoading: boolean;
  isError: boolean;
  data?: AlbumDiscoveryView | null;
}): AlbumDiscoveryViewState {
  const tracks = input.data?.tracks ?? [];
  const metadataPending = input.data?.metadata_pending === true;
  const metadataTerminal = input.data?.metadata_terminal === true;

  if (input.albumId == null) {
    return { status: "idle", tracks, metadataPending: false };
  }
  if (input.isError) {
    return { status: "error", tracks, metadataPending };
  }
  if (input.isLoading) {
    return { status: "loading", tracks, metadataPending };
  }
  return {
    status: metadataTerminal ? "terminal" : metadataPending ? "pending" : "warm",
    tracks,
    metadataPending,
  };
}

export function useLibraryAlbumDiscovery(albumId: number | null) {
  const query = useQuery<AlbumDiscoveryView>({
    queryKey: albumDiscoveryQueryKey(albumId),
    queryFn: () => getLibraryAlbumDiscovery(albumId!),
    enabled: albumId != null,
    staleTime: 5 * 60 * 1000, // 5m
    gcTime: 24 * 60 * 60 * 1000,
  });

  const view = albumDiscoveryViewState({
    albumId,
    isLoading: query.isLoading,
    isError: query.isError,
    data: query.data,
  });

  return { ...query, view };
}

// ─── Mutation ───────────────────────────────────────────────────────

export function useDownloadMissingForAlbum() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (albumId: number) => downloadMissingForAlbum(albumId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["downloads"] });
      queryClient.invalidateQueries({ queryKey: ["library"] });
    },
  });
}

// ─── Manual metadata refresh ────────────────────────────────────────

/** Minimal invalidator surface so refresh success handling is testable. */
export interface QueryInvalidator {
  invalidateQueries(filters: { queryKey: readonly unknown[] }): unknown;
}

/**
 * Thin, testable seam for the refresh mutation's network call. The request is
 * a one-line delegation to the API client; naming it lets the mutation wiring
 * be unit-tested without a React renderer (this repo's vitest setup has none).
 */
export function requestAlbumRefreshMetadata(albumId: number) {
  return refreshAlbumMetadata(albumId);
}

/**
 * Invalidates the album's discovery query so the freshly materialized tracklist
 * is re-read. Pure over an injected invalidator — no React required to test.
 */
export function applyAlbumRefreshSuccess(
  invalidator: QueryInvalidator,
  albumId: number,
): void {
  invalidator.invalidateQueries({ queryKey: albumDiscoveryQueryKey(albumId) });
}

/**
 * Enqueues a manual metadata refresh and, on success, invalidates that album's
 * discovery query. The refresh engine materializes in the background and emits
 * a `metadata_refresh` SSE event; `useAlbumRefreshEvents` handles the same
 * invalidation when the completion arrives after this request returns.
 */
export function useRefreshAlbumMetadata() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (albumId: number) => requestAlbumRefreshMetadata(albumId),
    onSuccess: (_data, albumId) => {
      applyAlbumRefreshSuccess(queryClient, albumId);
    },
  });
}
