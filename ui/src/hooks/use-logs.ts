import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { clearLogs, getLogs } from "../api/client";
import type { LogEntry } from "../api/types";

/**
 * Live log stream for the Logs settings tab.
 *
 * Seeds entries from the GET /api/logs snapshot, then appends new lines as
 * "log_line" SSE events. Entries carry a monotonic `seq` (increasing forever,
 * even after a buffer clear) so the snapshot and the live stream never
 * duplicate and never skip.
 */
export function useLogStream(limit = 500) {
  const queryClient = useQueryClient();

  const { data: snapshot, isLoading } = useQuery({
    queryKey: ["logs"] as const,
    queryFn: getLogs,
    staleTime: 30_000,
  });

  const [entries, setEntries] = useState<LogEntry[]>([]);
  const [connected, setConnected] = useState(false);
  const maxSeqRef = useRef(0);
  const limitRef = useRef(limit);

  useEffect(() => {
    limitRef.current = limit;
  }, [limit]);

  // Seed from the snapshot, merging with anything already streamed so a
  // refetch (window focus, invalidation) never rolls the view back. Both the
  // snapshot and the stream are sequences keyed by the monotonic `seq`, so
  // union + sort is exact — no duplicates, no gaps.
  useEffect(() => {
    if (!snapshot) return;
    const snap = snapshot.entries ?? [];
    if (snap.length > 0) {
      maxSeqRef.current = Math.max(maxSeqRef.current, snap[snap.length - 1].seq);
    }
    setEntries((prev) => {
      const bySeq = new Map<number, LogEntry>(prev.map((e) => [e.seq, e]));
      for (const e of snap) bySeq.set(e.seq, e);
      return [...bySeq.values()]
        .sort((a, b) => a.seq - b.seq)
        .slice(-limitRef.current);
    });
  }, [snapshot]);

  useEffect(() => {
    let url = "/api/events";
    try {
      const apiKey = localStorage.getItem("groovearr_api_key");
      if (apiKey) url += `?apikey=${encodeURIComponent(apiKey)}`;
    } catch {
      // Ignore localStorage access errors.
    }

    const es = new EventSource(url);
    es.onopen = () => setConnected(true);
    es.onerror = () => setConnected(false);

    es.addEventListener("log_line", (e: MessageEvent) => {
      try {
        const entry = JSON.parse(e.data) as LogEntry;
        if (typeof entry.seq !== "number" || entry.seq <= maxSeqRef.current) {
          return;
        }
        maxSeqRef.current = entry.seq;
        setEntries((prev) => [...prev, entry].slice(-limitRef.current));
      } catch {
        // Malformed event — ignore.
      }
    });

    return () => es.close();
  }, []);

  const clear = useMutation({
    mutationFn: clearLogs,
    onSuccess: () => {
      setEntries([]);
      queryClient.invalidateQueries({ queryKey: ["logs"] });
    },
  });

  const refresh = () => queryClient.invalidateQueries({ queryKey: ["logs"] });

  return {
    entries,
    connected,
    isLoading,
    clear: clear.mutate,
    clearPending: clear.isPending,
    refresh,
    path: snapshot?.path ?? "",
    level: snapshot?.level ?? "",
  };
}