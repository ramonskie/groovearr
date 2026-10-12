import { useMemo } from "react";
import {
  useInfiniteQuery,
  useQuery,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import {
  getLibraryTracks,
  getLibraryArtists,
  getLibraryAlbums,
  getLibraryArtist,
  getLibraryArtistAlbums,
  getLibraryArtistTracks,
  getLibraryAlbumDiscovery,
  downloadMissingForAlbum,
  refreshAlbumMetadata,
} from "../api/client";
import type {
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

export function useLibraryArtistAlbums(artistId: number | null) {
  return useQuery({
    queryKey: ["library", "artist", artistId, "albums"] as const,
    queryFn: () => getLibraryArtistAlbums(artistId!),
    enabled: artistId != null,
    ...libDefaults,
  });
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
 * Album discovery payload plus the additive `metadata_pending` flag the
 * catalog-backed endpoint returns when the provider tracklist has not been
 * materialized yet (a cold read). Declared in the hook layer so the shared
 * wire types stay untouched; the field is optional so older payloads remain
 * valid.
 */
export interface AlbumDiscoveryView extends AlbumDiscoveryResponse {
  metadata_pending?: boolean;
}

/** Coarse view state the album components switch on. */
export type AlbumDiscoveryStatus =
  | "idle" // no album selected
  | "loading" // cold: no data yet, full-screen spinner
  | "error"
  | "warm" // materialized: full provider tracklist
  | "pending"; // partial: local fallback shown, provider tracklist materializing

export interface AlbumDiscoveryViewState {
  status: AlbumDiscoveryStatus;
  /** Provider merged tracklist, or the local fallback while pending. */
  tracks: DiscoveryTrackEntry[];
  /** True while the provider tracklist is still materializing (partial). */
  metadataPending: boolean;
}

/** Canonical query key for one album's discovery payload. */
export function albumDiscoveryQueryKey(albumId: number | null) {
  return ["library", "album", albumId, "discovery"] as const;
}

/**
 * Pure mapping from the react-query result to the warm/cold/pending view state
 * (AGENTS §4 — the component switches on this, not on raw flags). Extracted so
 * it can be unit-tested without a React renderer.
 */
export function albumDiscoveryViewState(input: {
  albumId: number | null;
  isLoading: boolean;
  isError: boolean;
  data?: AlbumDiscoveryView | null;
}): AlbumDiscoveryViewState {
  const tracks = input.data?.tracks ?? [];
  const metadataPending = input.data?.metadata_pending === true;

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
    status: metadataPending ? "pending" : "warm",
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
