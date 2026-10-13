import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

// This repo's vitest runs in a plain node environment with no DOM renderer
// (no jsdom / @testing-library — see use-library.test.ts). Component render
// assertions use react-dom/server, and the discovery hooks are mocked so the
// artist header never hits the network. The infinite-scroll observer lives in
// an effect, which does not run during static rendering, so no
// IntersectionObserver shim is needed.
vi.mock("../../hooks/use-discovery", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("../../hooks/use-discovery")>();
  return {
    ...actual,
    useDiscoveryResolveArtist: vi.fn(),
    useArtistOverview: vi.fn(),
  };
});

import ArtistDetailView, {
  artistAlbumsRenderPlan,
} from "./ArtistDetailView";
import {
  useDiscoveryResolveArtist,
  useArtistOverview,
} from "../../hooks/use-discovery";
import type { Album, Artist } from "../../api/types";

// ─── Fixtures ───────────────────────────────────────────────────────

const artist: Artist = {
  id: 1,
  name: "Daft Punk",
  created_at: "",
  updated_at: "",
};

const album = (id: number, over: Partial<Album> = {}): Album => ({
  id,
  artist_id: 1,
  title: `Album ${id}`,
  track_count: 0,
  duration: 0,
  created_at: "",
  updated_at: "",
  ...over,
});

// ─── Mock defaults ──────────────────────────────────────────────────

function stubHooks() {
  vi.mocked(useDiscoveryResolveArtist).mockReturnValue({
    mutate: vi.fn(),
    isPending: false,
  } as unknown as ReturnType<typeof useDiscoveryResolveArtist>);
  vi.mocked(useArtistOverview).mockReturnValue({
    data: undefined,
    isLoading: false,
  } as unknown as ReturnType<typeof useArtistOverview>);
}

interface RenderOpts {
  albums?: Album[];
  artist?: Artist | undefined;
  isLoadingArtist?: boolean;
  isLoadingAlbums?: boolean;
  hasNextPage?: boolean;
  isFetchingNextPage?: boolean;
}

function renderView(opts: RenderOpts = {}): string {
  stubHooks();
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return renderToStaticMarkup(
    <MemoryRouter>
      <QueryClientProvider client={queryClient}>
        <ArtistDetailView
          artist={opts.artist === undefined ? artist : opts.artist}
          albums={opts.albums ?? []}
          tracks={[]}
          isLoadingArtist={opts.isLoadingArtist ?? false}
          isLoadingAlbums={opts.isLoadingAlbums ?? false}
          isLoadingTracks={false}
          hasNextPage={opts.hasNextPage}
          isFetchingNextPage={opts.isFetchingNextPage}
          onLoadMore={() => {}}
          onBack={() => {}}
          onSelectAlbum={() => {}}
        />
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
});

// ─── Pure render-plan mapping ───────────────────────────────────────

describe("artistAlbumsRenderPlan", () => {
  it("loading: spinner state, no sentinel", () => {
    const plan = artistAlbumsRenderPlan({
      isLoading: true,
      albums: [],
      hasNextPage: true,
      isFetchingNextPage: true,
    });
    expect(plan.kind).toBe("loading");
    expect(plan.showSentinel).toBe(false);
    expect(plan.showSentinelSpinner).toBe(false);
  });

  it("empty: empty state, no sentinel even when hasNextPage", () => {
    const plan = artistAlbumsRenderPlan({
      isLoading: false,
      albums: [],
      hasNextPage: true,
    });
    expect(plan.kind).toBe("empty");
    expect(plan.showSentinel).toBe(false);
  });

  it("grid without a next page: no sentinel", () => {
    const plan = artistAlbumsRenderPlan({
      isLoading: false,
      albums: [album(1)],
      hasNextPage: false,
    });
    expect(plan.kind).toBe("grid");
    expect(plan.showSentinel).toBe(false);
  });

  it("grid with a next page: sentinel shown, no spinner while idle", () => {
    const plan = artistAlbumsRenderPlan({
      isLoading: false,
      albums: [album(1)],
      hasNextPage: true,
      isFetchingNextPage: false,
    });
    expect(plan.kind).toBe("grid");
    expect(plan.showSentinel).toBe(true);
    expect(plan.showSentinelSpinner).toBe(false);
  });

  it("grid with a next page fetching: sentinel shows the spinner", () => {
    const plan = artistAlbumsRenderPlan({
      isLoading: false,
      albums: [album(1)],
      hasNextPage: true,
      isFetchingNextPage: true,
    });
    expect(plan.kind).toBe("grid");
    expect(plan.showSentinel).toBe(true);
    expect(plan.showSentinelSpinner).toBe(true);
  });
});

// ─── Component render (react-dom/server) ────────────────────────────

describe("ArtistDetailView album grid", () => {
  it("grid: renders the albums and the artist name", () => {
    const html = renderView({ albums: [album(1), album(2)] });
    expect(html).toContain("Album 1");
    expect(html).toContain("Album 2");
    // Artist name flows into each card from the artist context.
    expect(html).toContain("Daft Punk");
    expect(html).not.toContain("No albums found.");
  });

  it("empty: shows the empty state", () => {
    const html = renderView({ albums: [] });
    expect(html).toContain("No albums found.");
  });

  it("loading: shows a spinner, not the grid or empty state", () => {
    const html = renderView({ albums: [], isLoadingAlbums: true });
    expect(html).toContain('aria-label="Loading"');
    expect(html).not.toContain("No albums found.");
  });

  it("sentinel: rendered only when hasNextPage", () => {
    const withNext = renderView({ albums: [album(1)], hasNextPage: true });
    expect(withNext).toContain('data-testid="albums-sentinel"');

    const withoutNext = renderView({ albums: [album(1)], hasNextPage: false });
    expect(withoutNext).not.toContain('data-testid="albums-sentinel"');
  });

  it("sentinel spinner: shown while the next page is fetching", () => {
    const html = renderView({
      albums: [album(1)],
      hasNextPage: true,
      isFetchingNextPage: true,
    });
    expect(html).toContain('data-testid="albums-sentinel"');
    expect(html).toContain('aria-label="Loading"');
  });

  it("sentinel idle: no spinner while not fetching", () => {
    const html = renderView({
      albums: [album(1)],
      hasNextPage: true,
      isFetchingNextPage: false,
    });
    expect(html).toContain('data-testid="albums-sentinel"');
    expect(html).not.toContain('aria-label="Loading"');
  });

  it("preserves album selection affordances (selectable cards)", () => {
    const html = renderView({ albums: [album(1)] });
    // The selectable album card is a button labelled with the album title.
    expect(html).toContain("<button");
    expect(html).toContain("Album 1");
  });
});
