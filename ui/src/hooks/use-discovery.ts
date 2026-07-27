import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  getDiscoveryProviders,
  discoverySearch,
  resolveDiscoveryArtist,
  getArtistOverview,
  getArtistAlbums,
  getAlbumTracks,
  downloadAlbum,
} from "../api/client";
import type { ArtistOverview } from "../api/types";

const OVERVIEW_CACHE_PREFIX = "groovearr_overview_";

function overviewCacheKey(name: string): string {
  return OVERVIEW_CACHE_PREFIX + name.toLowerCase();
}

function readOverviewCache(name: string): ArtistOverview | undefined {
  try {
    const raw = sessionStorage.getItem(overviewCacheKey(name));
    if (!raw) return undefined;
    const entry = JSON.parse(raw);
    // 1h client-side TTL
    if (Date.now() - entry.ts > 60 * 60 * 1000) {
      sessionStorage.removeItem(overviewCacheKey(name));
      return undefined;
    }
    return entry.data;
  } catch {
    return undefined;
  }
}

function writeOverviewCache(name: string, data: ArtistOverview): void {
  try {
    sessionStorage.setItem(overviewCacheKey(name), JSON.stringify({ data, ts: Date.now() }));
  } catch { /* quota exceeded — ignore */ }
}

export function useDiscoveryProviders() {
  return useQuery({
    queryKey: ["discovery", "providers"],
    queryFn: getDiscoveryProviders,
  });
}

export function useDiscoverySearch() {
  return useMutation({
    mutationFn: ({ query, type }: { query: string; type?: string }) =>
      discoverySearch(query, type),
  });
}

/** Resolve a library artist name to a discovery provider artist. */
export function useDiscoveryResolveArtist() {
  return useMutation({
    mutationFn: (name: string) => resolveDiscoveryArtist(name),
  });
}

/** Fetch artist overview: top tracks + discography (backend 24h cache + sessionStorage instant). */
export function useArtistOverview(name: string | undefined) {
  return useQuery({
    queryKey: ["discovery", "artistOverview", name],
    queryFn: async () => {
      const data = await getArtistOverview(name!);
      writeOverviewCache(name!, data);
      return data;
    },
    initialData: () => name ? readOverviewCache(name) : undefined,
    enabled: !!name,
    staleTime: 1000 * 60 * 60, // 1h — background refetch after stale
    gcTime: 1000 * 60 * 60 * 24, // keep in memory 24h
  });
}

export function useArtistAlbums(artistId: string | null, provider?: string) {
  return useQuery({
    queryKey: ["discovery", "artistAlbums", artistId, provider],
    queryFn: () => getArtistAlbums(artistId!, provider),
    enabled: !!artistId,
  });
}

export function useAlbumTracks(albumId: string | null, provider?: string) {
  return useQuery({
    queryKey: ["discovery", "albumTracks", albumId, provider],
    queryFn: () => getAlbumTracks(albumId!, provider),
    enabled: !!albumId,
  });
}

export function useDownloadAlbum() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (albumId: string) => downloadAlbum(albumId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["downloads"] });
    },
  });
}
