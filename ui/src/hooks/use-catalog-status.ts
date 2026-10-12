import { useQuery } from "@tanstack/react-query";
import { getCatalogStatus } from "../api/client";

/**
 * Shared query for the materialized catalog / refresh-engine status.
 *
 * GET /api/catalog/status is admin-only, so callers pass `enabled` (normally
 * their `isAdmin`) and a non-admin issues no request. Polls fast only while rows
 * are actually due, and slowly at rest (a large future-scheduled backoff must
 * not keep the card at 2s).
 */
export function useCatalogStatus(enabled: boolean) {
  return useQuery({
    queryKey: ["catalog", "status"] as const,
    queryFn: getCatalogStatus,
    enabled,
    staleTime: 0,
    refetchInterval: (query) => {
      const status = query.state.data;
      if (!status) return 5000;
      // Fast only when there is work due now; the engine's `running` flag is the
      // process-lifetime loop flag (true from boot), so it cannot distinguish
      // busy from idle.
      return status.queue.due_now > 0 ? 2000 : 5000;
    },
  });
}
