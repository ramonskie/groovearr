import { useEffect, useRef, useCallback } from "react";
import { useQueryClient } from "@tanstack/react-query";

// ─── Backoff constants ──────────────────────────────────────────────

const INITIAL_BACKOFF_MS = 1_000;
const MAX_BACKOFF_MS = 30_000;
const BACKOFF_MULTIPLIER = 2;

// ─── Hook ───────────────────────────────────────────────────────────

/**
 * App-wide listener for import lifecycle SSE events.
 *
 * Imports can be started from any page, but the `import_completed` listener
 * previously lived in `use-download-events`, which is mounted only on
 * DownloadsPage. Tracking views (the wanted badge, album statuses) therefore
 * went stale when an import finished anywhere else.
 *
 * Mount exactly once at the app shell. On `import_completed` / `import_failed`
 * it invalidates the `["tracking"]` prefix, which also covers
 * `["tracking","wanted"]` and `["tracking","artist",id]`.
 *
 * Reconnects with exponential backoff; closes the EventSource on unmount.
 */
export function useTrackingEvents() {
  const queryClient = useQueryClient();

  const backoffRef = useRef(INITIAL_BACKOFF_MS);
  const esRef = useRef<EventSource | null>(null);
  const reconnectTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const mountedRef = useRef(true);

  const connect = useCallback(() => {
    if (!mountedRef.current) return;

    // Same-origin EventSource sends the session cookie automatically.
    const es = new EventSource("/api/events");
    esRef.current = es;

    const invalidateTracking = () => {
      if (!mountedRef.current) return;
      queryClient.invalidateQueries({ queryKey: ["tracking"] });
    };

    es.onopen = () => {
      if (!mountedRef.current) return;
      backoffRef.current = INITIAL_BACKOFF_MS;
    };

    // Import flips a tracked album's status to `downloaded` (post-import
    // handler); a failure can move it out of an in-flight state. Refresh
    // tracking views either way.
    es.addEventListener("import_completed", invalidateTracking);
    es.addEventListener("import_failed", invalidateTracking);

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
