import type {
  DiscoveryAlbum,
  DiscoveryTrack,
  UnmatchedReviewCandidate,
  UnmatchedReviewSignals,
} from "../../api/types";

export const EMPTY_SIGNALS: UnmatchedReviewSignals = {
  isrc_exact: false,
  provider_track_id_exact: false,
  title_matches: false,
  artist_matches: false,
  duration_known: false,
  duration_within_tolerance: false,
  album_title_matches: false,
  track_count_matches: false,
  track_number_matches: false,
};

export function formatDuration(ms?: number): string {
  if (!ms || ms <= 0) return "unknown length";
  const seconds = Math.round(ms / 1000);
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
}

export function providerLabel(provider: string): string {
  return provider ? provider.charAt(0).toUpperCase() + provider.slice(1) : "Provider";
}

export function candidateKey(
  trackID: number,
  c: UnmatchedReviewCandidate,
): string {
  return `${trackID}:${c.provider}:${c.provider_track_id}`;
}

export function releaseKey(album: DiscoveryAlbum): string {
  return `release:${album.provider_name}:${album.provider_id}`;
}

export function candidateFromSearch(
  album: DiscoveryAlbum,
  track: DiscoveryTrack,
): UnmatchedReviewCandidate {
  return {
    provider: album.provider_name,
    provider_track_id: track.provider_id,
    provider_album_id: album.provider_id,
    title: track.title,
    artist: track.artist_name,
    album: track.album_title || album.title,
    album_type: album.type,
    album_track_count: album.track_count,
    track_number: track.track_number,
    disc_number: track.disc_number,
    duration_ms: track.duration_ms,
    isrc: track.isrc,
    score: 0,
    auto: false,
    signals: EMPTY_SIGNALS,
  };
}
