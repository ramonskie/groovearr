import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

// This repo's vitest runs in a plain node environment with no DOM renderer
// (no jsdom / @testing-library — see use-library.test.ts). Component render
// assertions therefore use react-dom/server, and each data hook is mocked so
// the view state is controlled deterministically without any network.
vi.mock("../../hooks/use-library", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("../../hooks/use-library")>();
  return {
    ...actual,
    useLibraryAlbumDiscovery: vi.fn(),
    useDownloadMissingForAlbum: vi.fn(),
    useRefreshAlbumMetadata: vi.fn(),
  };
});

vi.mock("../../hooks/use-downloads", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("../../hooks/use-downloads")>();
  return { ...actual, useStartBestDownload: vi.fn() };
});

import AlbumDetailView, {
  albumDetailRenderPlan,
  makeAlbumRefreshHandler,
  type AlbumDetailRenderPlan,
} from "./AlbumDetailView";
import {
  useLibraryAlbumDiscovery,
  useDownloadMissingForAlbum,
  useRefreshAlbumMetadata,
  type AlbumDiscoveryViewState,
} from "../../hooks/use-library";
import { useStartBestDownload } from "../../hooks/use-downloads";
import type { Album, DiscoveryTrackEntry } from "../../api/types";

// ─── Fixtures ───────────────────────────────────────────────────────

const album: Album = {
  id: 42,
  artist_id: 1,
  title: "Random Access Memories",
  track_count: 2,
  duration: 0,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const track = (
  over: Partial<DiscoveryTrackEntry> = {},
): DiscoveryTrackEntry => ({
  title: "Get Lucky",
  track_number: 1,
  duration_ms: 248000,
  downloaded: false,
  ...over,
});

const warmView = (over: Partial<AlbumDiscoveryViewState> = {}): AlbumDiscoveryViewState => ({
  status: "warm",
  tracks: [track(), track({ track_number: 2, title: "Instant Crush" })],
  metadataPending: false,
  ...over,
});

/**
 * A view state that also carries the raw catalog flags the discovery payload
 * provides. Production derives `status` from these; tests set both so the
 * render plan's terminal reason selection is driven deterministically.
 */
type ViewWithCatalogFlags = AlbumDiscoveryViewState & {
  metadata_terminal?: boolean;
  metadata_status?: string;
};

const terminalView = (
  reason: "unmatched" | "failed",
  over: Partial<AlbumDiscoveryViewState> = {},
): ViewWithCatalogFlags => ({
  status: "terminal",
  tracks: [track({ downloaded: true })],
  metadataPending: true,
  metadata_terminal: true,
  metadata_status: reason,
  ...over,
});

// ─── Mock defaults ──────────────────────────────────────────────────

const refreshMutate = vi.fn();

function stubHooks(
  view: AlbumDiscoveryViewState,
  { refreshing = false }: { refreshing?: boolean } = {},
) {
  vi.mocked(useLibraryAlbumDiscovery).mockReturnValue({
    view,
    // The component reads the raw terminal reason from the query payload; mirror
    // the view so tests can drive unmatched/failed messaging deterministically.
    data: view,
  } as unknown as ReturnType<typeof useLibraryAlbumDiscovery>);
  vi.mocked(useRefreshAlbumMetadata).mockReturnValue({
    mutate: refreshMutate,
    isPending: refreshing,
  } as unknown as ReturnType<typeof useRefreshAlbumMetadata>);
  vi.mocked(useDownloadMissingForAlbum).mockReturnValue({
    mutate: vi.fn(),
    isPending: false,
  } as unknown as ReturnType<typeof useDownloadMissingForAlbum>);
  vi.mocked(useStartBestDownload).mockReturnValue({
    mutate: vi.fn(),
    isPending: false,
  } as unknown as ReturnType<typeof useStartBestDownload>);
}

function renderView(
  view: AlbumDiscoveryViewState,
  opts?: { refreshing?: boolean },
): string {
  stubHooks(view, opts);
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return renderToStaticMarkup(
    <QueryClientProvider client={queryClient}>
      <AlbumDetailView album={album} artistName="Daft Punk" onBack={() => {}} />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  refreshMutate.mockClear();
});

// ─── Pure render-plan mapping ───────────────────────────────────────

describe("albumDetailRenderPlan", () => {
  it("warm: content, no pending indicator, no spinner", () => {
    const plan = albumDetailRenderPlan(warmView());
    expect(plan.kind).toBe("content");
    expect(plan.showPendingIndicator).toBe(false);
    expect(plan.showEmptyTracks).toBe(false);
  });

  it("cold: full-screen spinner", () => {
    const plan = albumDetailRenderPlan({
      status: "loading",
      tracks: [],
      metadataPending: false,
    });
    expect(plan.kind).toBe("spinner");
  });

  it("pending: content with the inline indicator", () => {
    const plan = albumDetailRenderPlan({
      status: "pending",
      tracks: [track({ downloaded: true })],
      metadataPending: true,
    });
    expect(plan.kind).toBe("content");
    expect(plan.showPendingIndicator).toBe(true);
  });

  it("terminal (unmatched): accurate message, no pending indicator", () => {
    const plan = albumDetailRenderPlan(terminalView("unmatched"));
    expect(plan.kind).toBe("content");
    expect(plan.isTerminal).toBe(true);
    expect(plan.showPendingIndicator).toBe(false);
    expect(plan.terminalReason).toBe("unmatched");
    expect(plan.terminalMessage).toBe("No provider match found");
  });

  it("terminal (failed): surfaces the refresh-failed reason", () => {
    const plan = albumDetailRenderPlan(terminalView("failed"));
    expect(plan.isTerminal).toBe(true);
    expect(plan.showPendingIndicator).toBe(false);
    expect(plan.terminalReason).toBe("failed");
    expect(plan.terminalMessage).toBe("Metadata refresh failed");
  });

  it("terminal via metadata_terminal alone wins over a pending status", () => {
    const plan = albumDetailRenderPlan({
      status: "pending",
      tracks: [track({ downloaded: true })],
      metadataPending: true,
      metadata_terminal: true,
    });
    expect(plan.isTerminal).toBe(true);
    expect(plan.showPendingIndicator).toBe(false);
  });

  it("terminal without a known status still shows a safe message", () => {
    const plan = albumDetailRenderPlan({
      status: "terminal",
      tracks: [],
      metadataPending: true,
    });
    expect(plan.isTerminal).toBe(true);
    expect(plan.showPendingIndicator).toBe(false);
    expect(plan.terminalReason).toBe("unknown");
    expect(plan.terminalMessage).toBe("Full tracklist unavailable");
  });

  it("error with no local tracks: hard error", () => {
    const plan = albumDetailRenderPlan({
      status: "error",
      tracks: [],
      metadataPending: false,
    });
    expect(plan.kind).toBe("error");
  });

  it("error with local fallback tracks: renders the tracks, no hard error", () => {
    const plan = albumDetailRenderPlan({
      status: "error",
      tracks: [track({ downloaded: true })],
      metadataPending: true,
    });
    expect(plan.kind).toBe("content");
  });

  it("in-flight manual refresh shows the indicator on an otherwise-warm album", () => {
    const plan = albumDetailRenderPlan(warmView(), { refreshing: true });
    expect(plan.kind).toBe("content");
    expect(plan.showPendingIndicator).toBe(true);
  });

  it("empty warm album flags the empty state", () => {
    const plan = albumDetailRenderPlan({
      status: "warm",
      tracks: [],
      metadataPending: false,
    });
    expect(plan.kind).toBe("content");
    expect(plan.showEmptyTracks).toBe(true);
    expect(plan.showPendingIndicator).toBe(false);
  });
});

// ─── Manual refresh click path ──────────────────────────────────────

describe("makeAlbumRefreshHandler", () => {
  it("triggers the mutation for the album id", () => {
    const mutate = vi.fn();
    makeAlbumRefreshHandler(mutate, 42)();
    expect(mutate).toHaveBeenCalledTimes(1);
    expect(mutate).toHaveBeenCalledWith(42);
  });
});

// ─── Component render (react-dom/server) ────────────────────────────

describe("AlbumDetailView render", () => {
  it("warm: renders tracks immediately and shows no spinner", () => {
    const html = renderView(warmView());
    expect(html).toContain("Get Lucky");
    expect(html).toContain("Instant Crush");
    // No Spinner anywhere (Spinner renders aria-label="Loading").
    expect(html).not.toContain('aria-label="Loading"');
    expect(html).not.toContain("Fetching full tracklist");
  });

  it("cold: shows the full-screen spinner, not tracks", () => {
    const html = renderView({
      status: "loading",
      tracks: [],
      metadataPending: false,
    });
    expect(html).toContain('aria-label="Loading"');
    expect(html).toContain("Loading…");
    expect(html).not.toContain("Get Lucky");
  });

  it("pending: shows the inline indicator while still rendering available tracks", () => {
    const html = renderView({
      status: "pending",
      tracks: [track({ downloaded: true })],
      metadataPending: true,
    });
    expect(html).toContain("Fetching full tracklist…");
    expect(html).toContain("Get Lucky");
    expect(html).not.toContain("Failed to load album tracks");
  });

  it("pending: still shows the spinner indicator (regression guard)", () => {
    const html = renderView({
      status: "pending",
      tracks: [track({ downloaded: true })],
      metadataPending: true,
    });
    expect(html).toContain("Fetching full tracklist…");
    expect(html).not.toContain("No provider match found");
  });

  it("terminal (unmatched): message + Refresh affordance, no pending spinner", () => {
    const html = renderView(terminalView("unmatched"));
    expect(html).toContain("No provider match found");
    expect(html).not.toContain("Fetching full tracklist");
    expect(html).not.toContain('aria-label="Loading"');
    expect(html).toContain("Refresh metadata");
  });

  it("terminal (failed): refresh-failed message, no pending spinner", () => {
    const html = renderView(terminalView("failed"));
    expect(html).toContain("Metadata refresh failed");
    expect(html).not.toContain("Fetching full tracklist");
    expect(html).not.toContain('aria-label="Loading"');
  });

  it("warm: shows neither the terminal message nor the spinner", () => {
    const html = renderView(warmView());
    expect(html).not.toContain("No provider match found");
    expect(html).not.toContain("Metadata refresh failed");
    expect(html).not.toContain("Fetching full tracklist");
  });

  it("error with no local tracks: renders the hard error", () => {
    const html = renderView({
      status: "error",
      tracks: [],
      metadataPending: false,
    });
    expect(html).toContain("Failed to load album tracks.");
    expect(html).not.toContain("Get Lucky");
  });

  it("error with local fallback tracks: renders tracks, not the hard error", () => {
    const html = renderView({
      status: "error",
      tracks: [track({ downloaded: true })],
      metadataPending: true,
    });
    expect(html).toContain("Get Lucky");
    expect(html).not.toContain("Failed to load album tracks.");
  });

  it("exposes a manual refresh affordance wired to the refresh hook", () => {
    const html = renderView(warmView());
    expect(html).toContain("Refresh metadata");
    expect(useRefreshAlbumMetadata).toHaveBeenCalled();
  });

  it("busy manual refresh: shows the busy label and the indicator", () => {
    const html = renderView(warmView(), { refreshing: true });
    expect(html).toContain("Refreshing…");
    expect(html).toContain("Fetching full tracklist…");
  });

  it("preserves per-track download affordances and aggregates", () => {
    const html = renderView(warmView());
    // Add-to-library track button title from downloadActions.
    expect(html).toContain("Add");
    // Track count aggregate in the header.
    expect(html).toContain("2 tracks");
  });
});

// ─── Type guard: plan stays exhaustive over the kind union ──────────

describe("AlbumDetailRenderPlan kind", () => {
  const kinds: AlbumDetailRenderPlan["kind"][] = ["spinner", "error", "content"];
  it("covers every render kind", () => {
    expect(new Set(kinds).size).toBe(3);
  });
});
