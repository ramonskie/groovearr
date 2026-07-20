import { useMemo, useEffect, useRef } from "react";
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
import { useDownloadStore } from "../stores/download-poll";
import type {
  ImportPlaylistRequest,
  PlaylistTrack,
  PlaylistTrackDownloadStatus,
} from "../api/types";

// ─── Queries ────────────────────────────────────────────────────────

export function usePlaylists() {
  return useQuery({
    queryKey: ["playlists"] as const,
    queryFn: getPlaylists,
  });
}

export function usePlaylist(id: number) {
  const records = useDownloadStore((s) => s.records);
  const queryClient = useQueryClient();
  const prevCompleted = useRef<Set<string>>(new Set());

  // Invalidate playlist query when a download with playlist_id completes,
  // so track status (linked/unmatched) updates without manual refresh.
  useEffect(() => {
    const downloads = Object.values(records);
    for (const d of downloads) {
      if (!d.playlist_id || !d.state) continue;
      const key = `${d.id}|${d.state}`;
      if (d.state === "imported" && !prevCompleted.current.has(d.id)) {
        prevCompleted.current.add(d.id);
        queryClient.invalidateQueries({ queryKey: ["playlist", Number(d.playlist_id)] });
      }
    }
  }, [records, queryClient]);

  const query = useQuery({
    queryKey: ["playlist", id] as const,
    queryFn: () => getPlaylist(id),
    enabled: id > 0,
    refetchInterval: 5000, // poll for track linking updates
  });

  // Attach per-track download status by matching active downloads to
  // playlist track source_track_ids and artist+title.
  const tracksWithStatus = useMemo<PlaylistTrack[]>(() => {
    const tracks = query.data?.tracks ?? [];
    if (tracks.length === 0) return tracks;

    const downloads = Object.values(records);

    return tracks.map((track): PlaylistTrack => {
      if (track.track_id != null) {
        return { ...track, download_status: "linked" as const };
      }

      const status = computeTrackDownloadStatus(track, downloads);
      return { ...track, download_status: status };
    });
  }, [query.data?.tracks, records]);

  return {
    ...query,
    data: query.data
      ? { ...query.data, tracks: tracksWithStatus }
      : undefined,
  };
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

// ─── Helpers ────────────────────────────────────────────────────────

/**
 * Determine a playlist track's download status by matching it against
 * active download records. Matches on source_track_id or artist+title.
 */
function computeTrackDownloadStatus(
  track: PlaylistTrack,
  downloads: { id: string; state: string; title?: string; artist?: string; display_name?: string; track_id?: string }[],
): PlaylistTrackDownloadStatus {
  const trackKey = `${track.artist ?? ""}|${track.title ?? ""}`.toLowerCase();

  for (const d of downloads) {
    // Match by track_id if set.
    if (d.track_id && d.track_id === track.source_track_id) {
      return downloadStateToStatus(d.state);
    }
    // Match by artist+title.
    const dKey = `${d.artist ?? ""}|${d.title ?? ""}`.toLowerCase();
    if (dKey === trackKey) {
      return downloadStateToStatus(d.state);
    }
    // Fuzzy: check display_name contains title.
    if (d.display_name?.toLowerCase().includes(track.title.toLowerCase())) {
      return downloadStateToStatus(d.state);
    }
  }

  return "unmatched";
}

function downloadStateToStatus(state: string): PlaylistTrackDownloadStatus {
  switch (state) {
    case "queued":
      return "queued";
    case "downloading":
    case "importPending":
    case "importing":
      return "downloading";
    case "imported":
      return "linked";
    default:
      return "unmatched";
  }
}
