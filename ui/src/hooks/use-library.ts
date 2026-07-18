import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  getLibraryTracks,
  getLibraryArtists,
  getLibraryAlbums,
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
