import type {
  DiscoveryAlbum,
  DiscoveryTrack,
  UnmatchedAlbumReview,
} from "../../api/types";
import Button from "../../components/Button";
import {
  candidateFromSearch,
  candidateKey,
  formatDuration,
  providerLabel,
  releaseKey,
} from "./unmatched-review-helpers";

interface UnmatchedSearchSectionProps {
  review: UnmatchedAlbumReview;
  searchText: string;
  onSearchTextChange: (value: string) => void;
  onSearch: (query: string) => void;
  searching: boolean;
  searchError: boolean;
  searchSuccess: boolean;
  searchAlbums: DiscoveryAlbum[];
  selectedSearchAlbum: DiscoveryAlbum | null;
  onSelectAlbum: (album: DiscoveryAlbum | null) => void;
  tracksLoading: boolean;
  tracksError: boolean;
  tracksSuccess: boolean;
  searchTracks: DiscoveryTrack[];
  applyingKey: string | null;
  applying: boolean;
  onApplyRelease: (album: DiscoveryAlbum) => void;
  onApplyTrack: (track: DiscoveryTrack) => void;
}

export default function UnmatchedSearchSection({
  review,
  searchText,
  onSearchTextChange,
  onSearch,
  searching,
  searchError,
  searchSuccess,
  searchAlbums,
  selectedSearchAlbum,
  onSelectAlbum,
  tracksLoading,
  tracksError,
  tracksSuccess,
  searchTracks,
  applyingKey,
  applying,
  onApplyRelease,
  onApplyTrack,
}: UnmatchedSearchSectionProps) {
  return (
    <section className="border-t border-slate-800 pt-4">
      <h4 className="text-sm font-semibold text-white">Find on a provider</h4>
      <p className="mt-1 text-xs text-slate-500">
        Use this when the stored ID/ISRC points to the wrong recording. Search by
        the local artist and title, or by a title you recognize.
      </p>
      <form
        className="mt-2 flex gap-2"
        onSubmit={(event) => {
          event.preventDefault();
          const query = searchText.trim();
          if (!query) return;
          onSelectAlbum(null);
          onSearch(query);
        }}
      >
        <input
          type="text"
          aria-label="Search providers"
          value={searchText}
          onChange={(event) => onSearchTextChange(event.target.value)}
          placeholder="Artist, album, or track"
          className="min-w-0 flex-1 rounded-lg border border-slate-700 bg-slate-950 px-3 py-2 text-sm text-white placeholder:text-slate-600 focus:border-purple-500 focus:outline-none"
        />
        <Button
          type="submit"
          size="sm"
          loading={searching}
          disabled={!searchText.trim()}
        >
          Search
        </Button>
      </form>

      {searchError && (
        <p className="mt-2 text-xs text-red-400">
          Provider search failed — try again shortly.
        </p>
      )}

      {searchSuccess && (
        <div className="mt-3 space-y-2">
          {searchAlbums.length === 0 && (
            <p className="text-sm text-slate-500">
              No releases found. Try a shorter query.
            </p>
          )}
          {searchAlbums.map((album) => {
            const selected =
              selectedSearchAlbum?.provider_name === album.provider_name &&
              selectedSearchAlbum?.provider_id === album.provider_id;
            // MusicBrainz/Discogs search results omit track_count (0). Once
            // the selected release is loaded, its tracklist is authoritative.
            const releaseTrackCount =
              selected && tracksSuccess ? searchTracks.length : album.track_count;
            const tooSmall =
              review.track_count > 1 &&
              releaseTrackCount > 0 &&
              releaseTrackCount < review.track_count;
            const canApplyRelease =
              !tooSmall &&
              searchTracks.length > 0 &&
              (review.track_count > 1 || releaseTrackCount === 1);
            return (
              <div
                key={`${album.provider_name}:${album.provider_id}`}
                className={`rounded-lg border p-2 ${
                  selected
                    ? "border-purple-500 bg-purple-950/20"
                    : "border-slate-800 bg-slate-900/60"
                }`}
              >
                <button
                  type="button"
                  aria-pressed={selected}
                  onClick={() => onSelectAlbum(selected ? null : album)}
                  className="w-full text-left"
                >
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="rounded bg-slate-700 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-slate-200">
                      {providerLabel(album.provider_name)}
                    </span>
                    <span className="text-xs text-slate-500">
                      {album.type || "release"}
                      {album.year ? ` · ${album.year}` : ""}
                      {album.track_count
                        ? ` · ${album.track_count} tracks`
                        : ""}
                    </span>
                  </div>
                  <p className="mt-1 text-sm font-medium text-white">
                    {album.title}
                  </p>
                  <p className="text-xs text-slate-400">{album.artist_name}</p>
                </button>

                {selected && (
                  <div className="mt-2 border-t border-slate-800 pt-2">
                    {tracksLoading && (
                      <p className="text-xs text-slate-500">
                        Loading release tracks…
                      </p>
                    )}
                    {tracksError && (
                      <p className="text-xs text-red-400">
                        Could not load this release's tracks.
                      </p>
                    )}
                    {tracksSuccess && (
                      <>
                        {tooSmall && (
                          <p className="text-xs text-red-400">
                            This release has fewer tracks than the local album.
                            Choose a complete release instead.
                          </p>
                        )}
                        {canApplyRelease && (
                          <Button
                            size="sm"
                            loading={applyingKey === releaseKey(album)}
                            disabled={applying}
                            onClick={() => onApplyRelease(album)}
                          >
                            Use this release ({searchTracks.length} tracks)
                          </Button>
                        )}
                        <ul className="mt-2 space-y-1">
                          {searchTracks.map((track, index) => (
                            <li
                              key={`${track.provider_id}:${index}`}
                              className="flex items-center justify-between gap-2 rounded px-1 py-0.5 text-xs text-slate-300"
                            >
                              <span className="min-w-0 truncate">
                                {track.track_number || index + 1}. {track.title}
                                <span className="text-slate-500">
                                  {" "}
                                  · {formatDuration(track.duration_ms)}
                                </span>
                              </span>
                              {review.track_count === 1 &&
                                review.identity_providers.includes(
                                  album.provider_name,
                                ) && (
                                  <Button
                                    size="sm"
                                    variant="ghost"
                                    loading={
                                      applyingKey ===
                                      `search:${candidateKey(
                                        review.tracks[0]?.track_id ?? 0,
                                        candidateFromSearch(album, track),
                                      )}`
                                    }
                                    disabled={applying}
                                    onClick={() => onApplyTrack(track)}
                                  >
                                    Use track
                                  </Button>
                                )}
                            </li>
                          ))}
                        </ul>
                        {review.track_count === 1 &&
                          releaseTrackCount !== 1 &&
                          !review.identity_providers.includes(
                            album.provider_name,
                          ) && (
                            <p className="mt-2 text-xs text-amber-400">
                              This provider is not currently available for a
                              single-track match. Choose a one-track release,
                              or retry later.
                            </p>
                          )}
                      </>
                    )}
                  </div>
                )}
              </div>
            );
          })}
        </div>
      )}
    </section>
  );
}
