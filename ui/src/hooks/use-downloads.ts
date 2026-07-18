import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  getDownloads,
  download,
  downloadBest,
  cancelDownload,
} from "../api/client";
import { useDownloadPollStore } from "../stores/download-poll";
import type {
  DownloadRequest,
  DownloadBestRequest,
  DownloadRecord,
} from "../api/types";

// ─── Query ──────────────────────────────────────────────────────────

export function useDownloads() {
  const pollingActive = useDownloadPollStore((s) => s.pollingActive);
  return useQuery({
    queryKey: ["downloads"] as const,
    queryFn: getDownloads,
    refetchInterval: pollingActive ? 2000 : false,
    enabled: true,
  });
}

// ─── Mutations ──────────────────────────────────────────────────────

export function useStartDownload() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (payload: DownloadRequest) => download(payload),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["downloads"] });
    },
  });
}

export function useStartBestDownload() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (payload: DownloadBestRequest) => downloadBest(payload),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["downloads"] });
    },
  });
}

export function useCancelDownload() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => cancelDownload(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["downloads"] });
    },
  });
}

// ─── Clear completed ────────────────────────────────────────────────

const COMPLETED_STATES = new Set(["succeeded", "errored", "cancelled", "aborted"]);

export function useClearCompleted() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async () => {
      // Fetch current downloads and cancel each completed one
      const downloads: DownloadRecord[] = await getDownloads();
      const toCancel = downloads.filter((d) => COMPLETED_STATES.has(d.state));
      await Promise.allSettled(toCancel.map((d) => cancelDownload(d.id)));
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["downloads"] });
    },
  });
}
