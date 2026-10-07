import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";
import { getLogs } from "../api/client";
import type { LogEntry } from "../api/types";

/**
 * Live log stream for the Logs settings tab.
 *
 * The on-disk log file is the single source of truth. History comes from the
 * GET /api/logs snapshot (file tail); new lines are appended as "log_line"
 * SSE events pushed by a file tailer. Since the snapshot and the stream can
 * briefly overlap (a line written between the snapshot read and the SSE
 * connect arrives via both), entries are deduplicated by their raw line.
 * No server-side sequence number exists anymore.
 *
 * The raw line is unique for JSON logs (nanosecond timestamps). slog text
 * format has millisecond precision, so two byte-identical lines within the
 * same millisecond can still share a key and one is dropped — accepted, the
 * same window the previous content-key dedup had, and only reachable with
 * log_format=text.
 */
export function logEntryKey(e: LogEntry): string {
  return e.raw;
}

function byTime(a: LogEntry, b: LogEntry): number {
  return new Date(a.time).getTime() - new Date(b.time).getTime();
}

export function useLogStream(limit = 500) {
  const queryClient = useQueryClient();

  const { data: snapshot, isLoading } = useQuery({
    queryKey: ["logs"] as const,
    queryFn: getLogs,
    staleTime: 30_000,
  });

  const [entries, setEntries] = useState<LogEntry[]>([]);
  const [connected, setConnected] = useState(false);
  const limitRef = useRef(limit);
  const seenRef = useRef<Set<string>>(new Set());

  useEffect(() => {
    limitRef.current = limit;
  }, [limit]);

  // Seed from the file-tail snapshot. Refetching (window focus, invalidation)
  // merges by content key and sorts by time so the view never rolls back,
  // duplicates, or reorders entries. `limit` is a dependency so changing
  // log_captured_max re-slices the visible window live.
  useEffect(() => {
    if (!snapshot) return;
    const snap = snapshot.entries ?? [];
    if (snap.length === 0) return;
    setEntries((prev) => {
      const byKey = new Map<string, LogEntry>(prev.map((e) => [logEntryKey(e), e]));
      for (const e of snap) byKey.set(logEntryKey(e), e);
      const merged = [...byKey.values()].sort(byTime).slice(-limitRef.current);
      // Rebuild the dedup set from the merged view to keep it bounded; the
      // live stream keeps growing it, so this also resets the counter.
      seenRef.current = new Set(merged.map(logEntryKey));
      return merged;
    });
  }, [snapshot, limit]);

  useEffect(() => {
    // Same-origin EventSource sends the session cookie automatically.
    const es = new EventSource("/api/events");
    es.onopen = () => setConnected(true);
    es.onerror = () => setConnected(false);

    es.addEventListener("log_line", (e: MessageEvent) => {
      try {
        const entry = JSON.parse(e.data) as LogEntry;
        if (!entry || typeof entry.raw !== "string" || entry.raw === "") return;
        const k = logEntryKey(entry);
        if (seenRef.current.has(k)) return;
        seenRef.current.add(k);
        setEntries((prev) => {
          const next = [...prev, entry].slice(-limitRef.current);
          // Bound the dedup set: once it exceeds the visible window by a
          // margin, rebuild it from what the view actually keeps.
          if (seenRef.current.size > limitRef.current * 2) {
            seenRef.current = new Set(next.map(logEntryKey));
          }
          return next;
        });
      } catch {
        // Malformed event — ignore.
      }
    });

    return () => es.close();
  }, []);

  const refresh = () => queryClient.invalidateQueries({ queryKey: ["logs"] });

  const path = useMemo(() => snapshot?.path ?? "", [snapshot]);
  const level = useMemo(() => snapshot?.level ?? "", [snapshot]);

  return {
    entries,
    connected,
    isLoading,
    refresh,
    path,
    level,
  };
}
