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
  scanLibrary,
} from "../api/client";
import type { Artist } from "../api/types";

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

export function useLibraryAlbumDiscovery(albumId: number | null) {
  return useQuery({
    queryKey: ["library", "album", albumId, "discovery"] as const,
    queryFn: () => getLibraryAlbumDiscovery(albumId!),
    enabled: albumId != null,
    staleTime: 5 * 60 * 1000, // 5m
    gcTime: 24 * 60 * 60 * 1000,
  });
}

// ─── Mutation ───────────────────────────────────────────────────────

export function useScanLibrary() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: scanLibrary,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["library"] });
    },
  });
}

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
