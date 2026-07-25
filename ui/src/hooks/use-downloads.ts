import { useEffect } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  getDownloads,
  download,
  downloadBest,
  cancelDownload,
  retryDownload,
} from "../api/client";
import { useDownloadStore } from "../stores/download-poll";
import type {
  DownloadRequest,
  DownloadBestRequest,
  DownloadRecord,
} from "../api/types";

// ─── Query ──────────────────────────────────────────────────────────

/**
 * Hydrates the zustand download store with initial data from the API and keeps
 * it refreshed via polling when SSE is disconnected.
 *
 * Returns the **live** records from the zustand store (updated by SSE events),
 * plus loading/error state from the query.
 */
export function useDownloads() {
  const pollingActive = useDownloadStore((s) => s.pollingActive);
  const records = useDownloadStore((s) => s.records);
  const setRecords = useDownloadStore((s) => s.setRecords);
  const sseStatus = useDownloadStore((s) => s.sseStatus);

  const query = useQuery({
    queryKey: ["downloads"] as const,
    queryFn: getDownloads,
    refetchInterval: pollingActive ? 2000 : false,
    // Only poll when SSE is down.
    enabled: true,
    // Always refetch on mount — don't serve stale cached data.
    staleTime: 0,
    // Don't keep cache after unmount — fresh query on next mount.
    gcTime: 0,
    // Don't refetch on window focus — polling/SSE handles freshness.
    refetchOnWindowFocus: false,
  });

  // Sync query results into the store (initial load + polling fallback).
  useEffect(() => {
    if (query.data) {
      setRecords(query.data);
    }
  }, [query.data, setRecords]);

  const downloads: DownloadRecord[] = Object.values(records);

  return {
    ...query,
    data: downloads,
    /** True when SSE is the live-update path (connected). */
    sseActive: sseStatus === "connected",
    sseStatus,
  };
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
  const removeRecord = useDownloadStore((s) => s.removeRecord);
  return useMutation({
    mutationFn: (id: string) => cancelDownload(id),
    onSuccess: (_data, id) => {
      removeRecord(id);
      queryClient.invalidateQueries({ queryKey: ["downloads"] });
    },
  });
}

export function useRetryDownload() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => retryDownload(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["downloads"] });
    },
  });
}

// ─── Clear completed ────────────────────────────────────────────────

const COMPLETED_STATES = new Set(["imported", "failed", "ignored"]);

export function useClearCompleted() {
  const queryClient = useQueryClient();
  const records = useDownloadStore((s) => s.records);

  return useMutation({
    mutationFn: async () => {
      const arr = Object.values(records);
      const toCancel = arr.filter((d) => COMPLETED_STATES.has(d.state));
      await Promise.allSettled(toCancel.map((d) => cancelDownload(d.id)));
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["downloads"] });
    },
  });
}
