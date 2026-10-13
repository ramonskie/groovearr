import { useQuery } from "@tanstack/react-query";
import { getVersion } from "../api/client";

/**
 * Build version for the sidebar footer. The value is fixed for the life of the
 * process, so it is fetched once and cached indefinitely — it never needs a
 * refetch on focus, reconnect, or mount.
 */
export function useVersion() {
  return useQuery({
    queryKey: ["version"] as const,
    queryFn: getVersion,
    staleTime: Infinity,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  });
}
