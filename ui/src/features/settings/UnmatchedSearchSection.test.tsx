import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type {
  DiscoveryAlbum,
  DiscoveryTrack,
  UnmatchedAlbumReview,
} from "../../api/types";
import UnmatchedSearchSection from "./UnmatchedSearchSection";

const album: DiscoveryAlbum = {
  provider_id: "mb-release",
  provider_name: "musicbrainz",
  title: "Single",
  artist_name: "Artist",
  type: "single",
  year: 2020,
  track_count: 0, // search result omits the count
  cover_url: "",
};

const review: UnmatchedAlbumReview = {
  album_id: 1,
  artist: "Artist",
  title: "Single",
  track_count: 1,
  identity_providers: [], // this fixture exercises the release-search fallback
  tracks: [
    {
      track_id: 10,
      title: "Song",
      artist: "Artist",
      album: "Single",
      track_number: 1,
      disc_number: 1,
      duration_ms: 200000,
      isrc: "",
      candidates: [],
    },
  ],
};

function track(index: number): DiscoveryTrack {
  return {
    provider_id: `mb-track-${index}`,
    title: `Track ${index}`,
    artist_name: "Artist",
    album_title: "Single",
    track_number: index,
    disc_number: 1,
    duration_ms: 200000,
    isrc: "",
  };
}

function render(searchTracks: DiscoveryTrack[]) {
  return renderToStaticMarkup(
    <UnmatchedSearchSection
      review={review}
      searchText="Artist Single"
      onSearchTextChange={() => {}}
      onSearch={() => {}}
      searching={false}
      searchError={false}
      searchSuccess
      searchAlbums={[album]}
      selectedSearchAlbum={album}
      onSelectAlbum={() => {}}
      tracksLoading={false}
      tracksError={false}
      tracksSuccess
      searchTracks={searchTracks}
      applyingKey={null}
      applying={false}
      onApplyRelease={vi.fn()}
      onApplyTrack={vi.fn()}
    />,
  );
}

describe("UnmatchedSearchSection", () => {
  it("allows a release match when the fetched tracklist is a single", () => {
    const html = render([track(1)]);
    expect(html).toContain("Use this release (1 tracks)");
    expect(html).not.toContain("Use track");
  });

  it("hides the release match and warns when the fetched release is larger", () => {
    const html = render([track(1), track(2)]);
    expect(html).not.toContain("Use this release");
    expect(html).toContain("not currently available for a single-track match");
  });

  it("does not offer a release match for an empty fetched tracklist", () => {
    const html = render([]);
    expect(html).not.toContain("Use this release");
  });
});
