import { describe, expect, it } from "vitest";
import type { DiscoveryAlbum, DiscoveryTrack } from "../../api/types";
import {
  candidateFromSearch,
  candidateKey,
  formatDuration,
  providerLabel,
  releaseKey,
} from "./unmatched-review-helpers";

function album(overrides: Partial<DiscoveryAlbum> = {}): DiscoveryAlbum {
  return {
    provider_id: "sp-album",
    provider_name: "spotify",
    title: "WOW",
    artist_name: "DJ Antoine",
    type: "album",
    year: 2012,
    track_count: 17,
    cover_url: "",
    ...overrides,
  };
}

function track(overrides: Partial<DiscoveryTrack> = {}): DiscoveryTrack {
  return {
    provider_id: "sp-track",
    title: "Anywhere You Go",
    artist_name: "DJ Antoine",
    album_title: "WOW",
    track_number: 4,
    disc_number: 1,
    duration_ms: 214000,
    isrc: "CH4881102606",
    ...overrides,
  };
}

describe("formatDuration", () => {
  it("formats milliseconds and treats unknown/zero as unknown", () => {
    expect(formatDuration(214000)).toBe("3:34");
    expect(formatDuration(0)).toBe("unknown length");
    expect(formatDuration(undefined)).toBe("unknown length");
  });
});

describe("provider labels and keys", () => {
  it("capitalizes a provider label without changing the key", () => {
    expect(providerLabel("spotify")).toBe("Spotify");
    expect(providerLabel("")).toBe("Provider");
    expect(releaseKey(album())).toBe("release:spotify:sp-album");
  });

  it("uses provider + track id for a candidate key", () => {
    const candidate = candidateFromSearch(album(), track());
    expect(candidateKey(6500, candidate)).toBe("6500:spotify:sp-track");
  });
});

describe("candidateFromSearch", () => {
  it("maps a discovery track onto the review candidate wire shape", () => {
    const candidate = candidateFromSearch(
      album(),
      track({ album_title: "", isrc: undefined }),
    );
    expect(candidate).toMatchObject({
      provider: "spotify",
      provider_track_id: "sp-track",
      provider_album_id: "sp-album",
      title: "Anywhere You Go",
      artist: "DJ Antoine",
      album: "WOW",
      album_type: "album",
      album_track_count: 17,
      track_number: 4,
      disc_number: 1,
      duration_ms: 214000,
      score: 0,
      auto: false,
    });
    expect(candidate.isrc).toBeUndefined();
    expect(candidate.signals.isrc_exact).toBe(false);
  });
});
