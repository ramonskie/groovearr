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

import { ApiRequestError, refreshAlbumMetadata } from "../api/client";
import type { Album, DiscoveryTrackEntry } from "../api/types";
import {
  ALBUM_PAGE_SIZE,
  albumDiscoveryQueryKey,
  albumDiscoveryViewState,
  albumNextPageOffset,
  applyAlbumRefreshSuccess,
  flattenAlbumPages,
  isArtistAlbumsEnabled,
  isLibraryAlbumEnabled,
  isNotFoundError,
  libraryAlbumQueryKey,
  requestAlbumRefreshMetadata,
  shouldRetryLibraryAlbum,
} from "./use-library";
import { parseAlbumRefreshPayload } from "./use-album-refresh-events";

const track = (over: Partial<DiscoveryTrackEntry> = {}): DiscoveryTrackEntry => ({
  title: "Get Lucky",
  track_number: 1,
  duration_ms: 248000,
  downloaded: false,
  ...over,
});

const album = (id: number): Album => ({
  id,
  artist_id: 1,
  title: `Album ${id}`,
  track_count: 0,
  duration: 0,
  created_at: "",
  updated_at: "",
});

/** A fetched page holding `count` sentinel-inclusive rows, ids from `startId`. */
const makeAlbumPage = (count: number, startId = 1): Album[] =>
  Array.from({ length: count }, (_, i) => album(startId + i));

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

describe("albumDiscoveryViewState (warm / cold / pending / terminal mapping)", () => {
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
    expect(view.status).not.toBe("terminal");
    expect(view.metadataPending).toBe(true);
    expect(view.tracks).toHaveLength(1);
    expect(view.tracks[0].downloaded).toBe(true);
  });

  it("terminal when metadata_terminal is true, even while metadata_pending is true", () => {
    // Backend parks an unmatched/failed album with metadata_pending=true (its
    // provider tracklist never materialized) AND metadata_terminal=true. The
    // terminal flag must win so the UI stops showing a perpetual spinner.
    const view = albumDiscoveryViewState({
      albumId: 7,
      isLoading: false,
      isError: false,
      data: {
        tracks: [track({ downloaded: true })],
        metadata_pending: true,
        metadata_status: "unmatched",
        metadata_terminal: true,
      },
    });
    expect(view.status).toBe("terminal");
    expect(view.status).not.toBe("pending");
    expect(view.metadataPending).toBe(true);
    expect(view.tracks).toHaveLength(1);
  });

  it("terminal carries the failed status through as well", () => {
    const view = albumDiscoveryViewState({
      albumId: 7,
      isLoading: false,
      isError: false,
      data: { tracks: [], metadata_pending: true, metadata_status: "failed", metadata_terminal: true },
    });
    expect(view.status).toBe("terminal");
    expect(view.metadataPending).toBe(true);
  });

  it("terminal does not mask a hard error", () => {
    const view = albumDiscoveryViewState({
      albumId: 7,
      isLoading: false,
      isError: true,
      data: { tracks: [], metadata_pending: true, metadata_terminal: true },
    });
    expect(view.status).toBe("error");
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

  it("legacy payload with neither new field still maps to warm", () => {
    // Pre-02 backend: no metadata_status / metadata_terminal keys at all.
    const view = albumDiscoveryViewState({
      albumId: 7,
      isLoading: false,
      isError: false,
      data: { tracks: [track(), track({ track_number: 2 })] },
    });
    expect(view.status).toBe("warm");
    expect(view.metadataPending).toBe(false);
    expect(view.tracks).toHaveLength(2);
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

describe("useLibraryArtistAlbums infinite-query helpers", () => {
  it("first page: drops the page+1 sentinel and returns only ALBUM_PAGE_SIZE rows", () => {
    const firstPage = makeAlbumPage(ALBUM_PAGE_SIZE + 1);
    expect(flattenAlbumPages([firstPage])).toHaveLength(ALBUM_PAGE_SIZE);
  });

  it("first page: advances the offset when the sentinel is present", () => {
    const firstPage = makeAlbumPage(ALBUM_PAGE_SIZE + 1);
    expect(albumNextPageOffset(firstPage, [firstPage])).toBe(ALBUM_PAGE_SIZE);
  });

  it("next-page detection: last page <= ALBUM_PAGE_SIZE has no next page", () => {
    const full = makeAlbumPage(ALBUM_PAGE_SIZE + 1);
    const short = makeAlbumPage(ALBUM_PAGE_SIZE); // exactly page size -> exhausted
    expect(albumNextPageOffset(short, [full, short])).toBeUndefined();
  });

  it("next-page detection: no last page (empty result) has no next page", () => {
    expect(albumNextPageOffset(undefined, [])).toBeUndefined();
  });

  it("accumulates the offset across pages and slices every page", () => {
    const p1 = makeAlbumPage(ALBUM_PAGE_SIZE + 1, 1);
    const p2 = makeAlbumPage(ALBUM_PAGE_SIZE + 1, 1000);
    expect(albumNextPageOffset(p2, [p1, p2])).toBe(2 * ALBUM_PAGE_SIZE);
    expect(flattenAlbumPages([p1, p2])).toHaveLength(2 * ALBUM_PAGE_SIZE);
  });

  it("flatten tolerates undefined / empty pages", () => {
    expect(flattenAlbumPages(undefined)).toEqual([]);
    expect(flattenAlbumPages([])).toEqual([]);
  });

  it("null artist: the query gate is disabled; a selected artist enables it", () => {
    expect(isArtistAlbumsEnabled(null)).toBe(false);
    expect(isArtistAlbumsEnabled(42)).toBe(true);
  });
});

// The real `useQuery`/`useInfiniteQuery` wiring (query key, enabled gate,
// getNextPageParam) is only covered here through extracted pure helpers: this
// repo's vitest setup has no DOM renderer (`@testing-library/react` / jsdom are
// not dependencies), so the hooks themselves cannot be rendered. These helpers
// are the exact expressions the hooks pass to react-query, so the wiring is
// exercised without a renderer.
describe("useLibraryAlbum (single-album by-id query) helpers", () => {
  it("builds the canonical album key", () => {
    expect(libraryAlbumQueryKey(42)).toEqual(["library", "album", 42]);
  });

  it("keeps the null key stable for a disabled query", () => {
    expect(libraryAlbumQueryKey(null)).toEqual(["library", "album", null]);
  });

  it("null album: the query gate is disabled; a selected album enables it", () => {
    expect(isLibraryAlbumEnabled(null)).toBe(false);
    expect(isLibraryAlbumEnabled(7)).toBe(true);
  });

  it("album already in the loaded grid: the by-id query is skipped", () => {
    expect(isLibraryAlbumEnabled(7, true)).toBe(false);
    // grid false / not-yet-loaded keeps the deep-link fetch alive
    expect(isLibraryAlbumEnabled(7, false)).toBe(true);
    expect(isLibraryAlbumEnabled(null, true)).toBe(false);
  });
});

describe("isNotFoundError (404 vs transient classification)", () => {
  it("true for an HTTP 404 from the API client", () => {
    expect(isNotFoundError(new ApiRequestError("album not found", 404))).toBe(true);
  });

  it("false for other HTTP statuses (500 / 403 / 401)", () => {
    expect(isNotFoundError(new ApiRequestError("server error", 500))).toBe(false);
    expect(isNotFoundError(new ApiRequestError("forbidden", 403))).toBe(false);
    expect(isNotFoundError(new ApiRequestError("session expired", 401))).toBe(false);
  });

  it("false for a network/plain error or non-error", () => {
    expect(isNotFoundError(new Error("network down"))).toBe(false);
    expect(isNotFoundError(undefined)).toBe(false);
    expect(isNotFoundError(null)).toBe(false);
    // only the API client's typed error carries the status contract
    expect(isNotFoundError({ status: 404 })).toBe(false);
  });
});

describe("shouldRetryLibraryAlbum (retry policy)", () => {
  it("never retries a 404, even on the first failure", () => {
    const err = new ApiRequestError("album not found", 404);
    expect(shouldRetryLibraryAlbum(0, err)).toBe(false);
    expect(shouldRetryLibraryAlbum(1, err)).toBe(false);
    expect(shouldRetryLibraryAlbum(2, err)).toBe(false);
  });

  it("keeps react-query's default three-attempt backoff for transient errors", () => {
    const err = new Error("network down");
    expect(shouldRetryLibraryAlbum(0, err)).toBe(true);
    expect(shouldRetryLibraryAlbum(1, err)).toBe(true);
    expect(shouldRetryLibraryAlbum(2, err)).toBe(true);
    expect(shouldRetryLibraryAlbum(3, err)).toBe(false);
  });
});
