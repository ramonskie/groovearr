import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  getLibraryTracks,
  getLibraryArtists,
  getLibraryAlbums,
  getLibraryArtist,
  getLibraryArtistAlbums,
  getLibraryArtistTracks,
  scanLibrary,
} from "../api/client";

// ─── Queries ────────────────────────────────────────────────────────

export function useLibraryTracks(q?: string) {
  return useQuery({
    queryKey: ["library", "tracks", q] as const,
    queryFn: () => getLibraryTracks({ q }),
  });
}

export function useLibraryArtists(q?: string) {
  return useQuery({
    queryKey: ["library", "artists", q] as const,
    queryFn: () => getLibraryArtists({ q }),
  });
}

export function useLibraryAlbums(q?: string) {
  return useQuery({
    queryKey: ["library", "albums", q] as const,
    queryFn: () => getLibraryAlbums({ q }),
  });
}

export function useLibraryArtist(artistId: number | null) {
  return useQuery({
    queryKey: ["library", "artist", artistId] as const,
    queryFn: () => getLibraryArtist(artistId!),
    enabled: artistId != null,
  });
}

export function useLibraryArtistAlbums(artistId: number | null) {
  return useQuery({
    queryKey: ["library", "artist", artistId, "albums"] as const,
    queryFn: () => getLibraryArtistAlbums(artistId!),
    enabled: artistId != null,
  });
}

export function useLibraryArtistTracks(artistId: number | null) {
  return useQuery({
    queryKey: ["library", "artist", artistId, "tracks"] as const,
    queryFn: () => getLibraryArtistTracks(artistId!),
    enabled: artistId != null,
  });
}

// ─── Mutation ───────────────────────────────────────────────────────

export function useScanLibrary() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: scanLibrary,
    onSuccess: () => {
      // Invalidate all library queries after a scan completes
      queryClient.invalidateQueries({ queryKey: ["library"] });
    },
  });
}
