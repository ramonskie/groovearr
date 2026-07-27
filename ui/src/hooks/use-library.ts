import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
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

// ─── Shared cache config ────────────────────────────────────────────

/** Library data rarely changes (only on scan) — keep fresh for 30m, cache for 24h. */
const libDefaults = {
  staleTime: 30 * 60 * 1000,       // 30m — refetch in background after this
  gcTime: 24 * 60 * 60 * 1000,     // 24h — don't garbage collect
} as const;

// ─── Queries ────────────────────────────────────────────────────────

export function useLibraryTracks(q?: string) {
  return useQuery({
    queryKey: ["library", "tracks", q] as const,
    queryFn: () => getLibraryTracks({ q }),
    ...libDefaults,
  });
}

export function useLibraryArtists(q?: string) {
  return useQuery({
    queryKey: ["library", "artists", q] as const,
    queryFn: () => getLibraryArtists({ q }),
    ...libDefaults,
  });
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
