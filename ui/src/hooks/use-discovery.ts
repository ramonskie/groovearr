import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  getDiscoveryProviders,
  discoverySearch,
  getArtistAlbums,
  getAlbumTracks,
  downloadAlbum,
} from "../api/client";

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
