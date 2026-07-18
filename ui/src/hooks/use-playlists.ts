import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  getPlaylists,
  getPlaylist,
  getPlaylistSources,
  browsePlaylistSource,
  importPlaylist,
  downloadMissing,
  syncPlaylist,
  deletePlaylist,
} from "../api/client";
import type {
  ImportPlaylistRequest,
} from "../api/types";

// ─── Queries ────────────────────────────────────────────────────────

export function usePlaylists() {
  return useQuery({
    queryKey: ["playlists"] as const,
    queryFn: getPlaylists,
  });
}

export function usePlaylist(id: number) {
  return useQuery({
    queryKey: ["playlist", id] as const,
    queryFn: () => getPlaylist(id),
    enabled: id > 0,
  });
}

export function usePlaylistSources() {
  return useQuery({
    queryKey: ["playlists", "sources"] as const,
    queryFn: getPlaylistSources,
  });
}

export function useBrowsePlaylistSource(source: string) {
  return useQuery({
    queryKey: ["playlists", "sources", source] as const,
    queryFn: () => browsePlaylistSource(source),
    enabled: source.length > 0,
  });
}

// ─── Mutations ──────────────────────────────────────────────────────

export function useImportPlaylist() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (payload: ImportPlaylistRequest) => importPlaylist(payload),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["playlists"] });
    },
  });
}

export function useDownloadMissing() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (playlistId: number) => downloadMissing(playlistId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["playlists"] });
    },
  });
}

export function useSyncPlaylist() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (playlistId: number) => syncPlaylist(playlistId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["playlists"] });
    },
  });
}

export function useDeletePlaylist() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (playlistId: number) => deletePlaylist(playlistId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["playlists"] });
    },
  });
}
