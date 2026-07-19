import { useEffect, useRef, useCallback } from "react";
import { useQueryClient } from "@tanstack/react-query";
import type { DownloadRecord } from "../api/types";
import { useDownloadStore } from "../stores/download-poll";

// ─── Backoff constants ──────────────────────────────────────────────

const INITIAL_BACKOFF_MS = 1_000;
const MAX_BACKOFF_MS = 30_000;
const BACKOFF_MULTIPLIER = 2;

// ─── Hook ───────────────────────────────────────────────────────────

/**
 * Connects to GET /api/events as an EventSource, parses SSE messages, and
 * dispatches download records into the zustand download store.
 *
 * On connection failure the hook enters polling-fallback mode via the store's
 * `startPolling()`. Exponential backoff is used for reconnection attempts.
 *
 * Cleanup on unmount: closes the EventSource and stops polling.
 */
export function useDownloadEvents() {
  // Use getState() to access stable action references — avoids re-running the
  // effect when store state (records/activeCount) changes.
  const storeRef = useRef(useDownloadStore.getState());
  const queryClient = useQueryClient();

  // Refresh ref on each render so callbacks always see latest actions.
  storeRef.current = useDownloadStore.getState();

  const backoffRef = useRef(INITIAL_BACKOFF_MS);
  const esRef = useRef<EventSource | null>(null);
  const reconnectTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const mountedRef = useRef(true);

  const connect = useCallback(() => {
    if (!mountedRef.current) return;

    const store = storeRef.current;
    store.setSseStatus("connecting");

    const es = new EventSource("/api/events");
    esRef.current = es;

    es.onopen = () => {
      if (!mountedRef.current) return;
      backoffRef.current = INITIAL_BACKOFF_MS;
      storeRef.current.setSseStatus("connected");
      storeRef.current.stopPolling();
    };

    es.addEventListener("download_queued", (e: MessageEvent) => {
      handleEvent(e, (r) => storeRef.current.upsertRecord(r));
    });
    es.addEventListener("download_stateChanged", (e: MessageEvent) => {
      handleEvent(e, (r) => storeRef.current.upsertRecord(r));
    });
    es.addEventListener("download_progress", (e: MessageEvent) => {
      handleEvent(e, (r) => storeRef.current.upsertRecord(r));
    });
    es.addEventListener("download_completed", (e: MessageEvent) => {
      handleEvent(e, (r) => storeRef.current.upsertRecord(r));
    });
    es.addEventListener("download_failed", (e: MessageEvent) => {
      handleEvent(e, (r) => storeRef.current.upsertRecord(r));
    });
    es.addEventListener("import_completed", (e: MessageEvent) => {
      handleEvent(e, (r) => storeRef.current.upsertRecord(r));
      queryClient.invalidateQueries({ queryKey: ["library"] });
    });

    es.onerror = () => {
      if (!mountedRef.current) return;
      es.close();
      esRef.current = null;
      storeRef.current.setSseStatus("disconnected");
      storeRef.current.startPolling();
      scheduleReconnect();
    };
  }, []); // Stable — all mutable deps accessed via refs.

  const scheduleReconnect = useCallback(() => {
    if (!mountedRef.current) return;
    reconnectTimerRef.current = setTimeout(() => {
      backoffRef.current = Math.min(
        backoffRef.current * BACKOFF_MULTIPLIER,
        MAX_BACKOFF_MS,
      );
      connect();
    }, backoffRef.current);
  }, [connect]);

  useEffect(() => {
    mountedRef.current = true;
    connect();

    return () => {
      mountedRef.current = false;
      if (reconnectTimerRef.current) clearTimeout(reconnectTimerRef.current);
      if (esRef.current) {
        esRef.current.close();
        esRef.current = null;
      }
      storeRef.current.setSseStatus("disconnected");
      storeRef.current.stopPolling();
    };
  }, [connect]);
}

// ─── Helpers ────────────────────────────────────────────────────────

function handleEvent(e: MessageEvent, upsert: (r: DownloadRecord) => void) {
  try {
    // Browser EventSource delivers the content of SSE "data:" lines as e.data.
    // The backend writes the JSON-marshalled DownloadRecord directly after "data:".
    const record = JSON.parse(e.data) as DownloadRecord;
    if (record && typeof record.id === "string") {
      upsert(record);
    }
  } catch {
    // Malformed event — ignore.
  }
}
