import { useEffect, useRef, useCallback } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { albumDiscoveryQueryKey } from "./use-library";

// ─── Backoff constants ──────────────────────────────────────────────

const INITIAL_BACKOFF_MS = 1_000;
const MAX_BACKOFF_MS = 30_000;
const BACKOFF_MULTIPLIER = 2;

// ─── Payload parsing ────────────────────────────────────────────────

/**
 * Parses the `metadata_refresh` SSE payload. The backend broadcasts
 * `{"album_id": <number>}` (cmd/groovearr/app.go) when the refresh engine
 * materializes an album. Returns the album id, or null for anything malformed
 * so the caller can ignore the event instead of crashing the stream handler.
 */
export function parseAlbumRefreshPayload(data: string): number | null {
  try {
    const parsed = JSON.parse(data) as { album_id?: unknown };
    const albumId = parsed?.album_id;
    if (
      typeof albumId === "number" &&
      Number.isInteger(albumId) &&
      albumId > 0
    ) {
      return albumId;
    }
  } catch {
    // Malformed event — ignore.
  }
  return null;
}

// ─── Hook ───────────────────────────────────────────────────────────

/**
 * Listens for `metadata_refresh` SSE events on GET /api/events and invalidates
 * the matching `["library","album",album_id,"discovery"]` query once an album's
 * provider tracklist has been materialized.
 *
 * Mount exactly once at the app shell (same pattern as useTrackingEvents /
 * useDownloadEvents). Reconnects with exponential backoff and closes the
 * EventSource on unmount. Guarded so SSR / environments without EventSource
 * (or vitest) simply no-op instead of throwing.
 */
export function useAlbumRefreshEvents() {
  const queryClient = useQueryClient();

  const backoffRef = useRef(INITIAL_BACKOFF_MS);
  const esRef = useRef<EventSource | null>(null);
  const reconnectTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const mountedRef = useRef(true);

  const connect = useCallback(() => {
    if (!mountedRef.current) return;
    // SSR / environments without EventSource (older browsers, node tests).
    if (typeof EventSource === "undefined") return;

    // Same-origin EventSource sends the session cookie automatically.
    const es = new EventSource("/api/events");
    esRef.current = es;

    es.onopen = () => {
      if (!mountedRef.current) return;
      backoffRef.current = INITIAL_BACKOFF_MS;
    };

    es.addEventListener("metadata_refresh", (e: MessageEvent) => {
      if (!mountedRef.current) return;
      const albumId = parseAlbumRefreshPayload(e.data);
      if (albumId == null) return;
      queryClient.invalidateQueries({
        queryKey: albumDiscoveryQueryKey(albumId),
      });
    });

    es.onerror = () => {
      if (!mountedRef.current) return;
      es.close();
      esRef.current = null;
      scheduleReconnect();
    };
  }, [queryClient]); // queryClient is stable; everything else goes through refs.

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
    };
  }, [connect]);
}
