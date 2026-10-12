import { describe, it, expect, vi, beforeEach } from "vitest";

// Mock only the network call under test; keep the rest of the client intact so
// importing use-library (which imports many client helpers) stays valid.
vi.mock("../api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api/client")>();
  return {
    ...actual,
    refreshAlbumMetadata: vi.fn(() => Promise.resolve({ queued: true })),
  };
});

import { refreshAlbumMetadata } from "../api/client";
import type { DiscoveryTrackEntry } from "../api/types";
import {
  albumDiscoveryQueryKey,
  albumDiscoveryViewState,
  applyAlbumRefreshSuccess,
  requestAlbumRefreshMetadata,
} from "./use-library";
import { parseAlbumRefreshPayload } from "./use-album-refresh-events";

const track = (over: Partial<DiscoveryTrackEntry> = {}): DiscoveryTrackEntry => ({
  title: "Get Lucky",
  track_number: 1,
  duration_ms: 248000,
  downloaded: false,
  ...over,
});

describe("albumDiscoveryQueryKey", () => {
  it("builds the canonical album discovery key", () => {
    expect(albumDiscoveryQueryKey(42)).toEqual([
      "library",
      "album",
      42,
      "discovery",
    ]);
  });

  it("keeps the null key stable for a disabled query", () => {
    expect(albumDiscoveryQueryKey(null)).toEqual([
      "library",
      "album",
      null,
      "discovery",
    ]);
  });
});

describe("albumDiscoveryViewState (warm / cold / pending mapping)", () => {
  it("idle when no album is selected", () => {
    const view = albumDiscoveryViewState({
      albumId: null,
      isLoading: false,
      isError: false,
      data: null,
    });
    expect(view.status).toBe("idle");
    expect(view.tracks).toEqual([]);
    expect(view.metadataPending).toBe(false);
  });

  it("cold => loading when there is no data yet", () => {
    const view = albumDiscoveryViewState({
      albumId: 7,
      isLoading: true,
      isError: false,
      data: undefined,
    });
    expect(view.status).toBe("loading");
    expect(view.tracks).toEqual([]);
  });

  it("warm when data is materialized (metadata_pending false)", () => {
    const view = albumDiscoveryViewState({
      albumId: 7,
      isLoading: false,
      isError: false,
      data: {
        provider: "deezer",
        tracks: [track(), track({ track_number: 2, title: "Instant Crush" })],
        metadata_pending: false,
      },
    });
    expect(view.status).toBe("warm");
    expect(view.metadataPending).toBe(false);
    expect(view.tracks).toHaveLength(2);
  });

  it("pending when metadata is still materializing (partial local fallback)", () => {
    const view = albumDiscoveryViewState({
      albumId: 7,
      isLoading: false,
      isError: false,
      data: { tracks: [track({ downloaded: true })], metadata_pending: true },
    });
    expect(view.status).toBe("pending");
    expect(view.metadataPending).toBe(true);
    expect(view.tracks).toHaveLength(1);
    expect(view.tracks[0].downloaded).toBe(true);
  });

  it("warm when metadata_pending is absent (older payload)", () => {
    const view = albumDiscoveryViewState({
      albumId: 7,
      isLoading: false,
      isError: false,
      data: { tracks: [track()] },
    });
    expect(view.status).toBe("warm");
    expect(view.metadataPending).toBe(false);
  });

  it("error wins even when stale data is present", () => {
    const view = albumDiscoveryViewState({
      albumId: 7,
      isLoading: false,
      isError: true,
      data: { tracks: [track()], metadata_pending: true },
    });
    expect(view.status).toBe("error");
  });
});

describe("parseAlbumRefreshPayload", () => {
  it("parses a valid album_id payload", () => {
    expect(parseAlbumRefreshPayload('{"album_id":42}')).toBe(42);
  });

  it("rejects malformed JSON", () => {
    expect(parseAlbumRefreshPayload("not json")).toBeNull();
  });

  it("rejects a missing / non-numeric album_id", () => {
    expect(parseAlbumRefreshPayload("{}")).toBeNull();
    expect(parseAlbumRefreshPayload('{"album_id":"42"}')).toBeNull();
    expect(parseAlbumRefreshPayload('{"album_id":null}')).toBeNull();
  });

  it("rejects non-positive or non-integer album_id", () => {
    expect(parseAlbumRefreshPayload('{"album_id":0}')).toBeNull();
    expect(parseAlbumRefreshPayload('{"album_id":-3}')).toBeNull();
    expect(parseAlbumRefreshPayload('{"album_id":1.5}')).toBeNull();
  });
});

describe("manual refresh mutation wiring", () => {
  beforeEach(() => {
    vi.mocked(refreshAlbumMetadata).mockClear();
  });

  it("calls the API client with the album id", async () => {
    const result = await requestAlbumRefreshMetadata(99);
    expect(refreshAlbumMetadata).toHaveBeenCalledTimes(1);
    expect(refreshAlbumMetadata).toHaveBeenCalledWith(99);
    expect(result).toEqual({ queued: true });
  });

  it("invalidates the matching album discovery query on success", () => {
    const invalidateQueries = vi.fn();
    applyAlbumRefreshSuccess({ invalidateQueries }, 99);
    expect(invalidateQueries).toHaveBeenCalledTimes(1);
    expect(invalidateQueries).toHaveBeenCalledWith({
      queryKey: ["library", "album", 99, "discovery"],
    });
  });
});
