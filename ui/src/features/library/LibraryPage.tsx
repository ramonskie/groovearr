import { useState, useRef, useEffect, useCallback, useMemo } from "react";
import { useSearchParams } from "react-router-dom";
import { Search } from "lucide-react";
import Spinner from "../../components/Spinner";
import StatusMessage from "../../components/StatusMessage";
import {
  useLibraryArtists,
  useLibraryArtist,
  useLibraryArtistAlbums,
  useLibraryArtistTracks,
  useLibraryAlbum,
  isNotFoundError,
} from "../../hooks/use-library";
import ArtistsSection from "./ArtistsSection";
import ArtistDetailView from "./ArtistDetailView";
import AlbumDetailView from "./AlbumDetailView";

export default function LibraryPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const selectedArtistId: number | null = (() => {
    const p = searchParams.get("artist");
    if (!p) return null;
    const n = parseInt(p, 10);
    return isNaN(n) ? null : n;
  })();
  const selectedAlbumId: number | null = (() => {
    const p = searchParams.get("album");
    if (!p) return null;
    const n = parseInt(p, 10);
    return isNaN(n) ? null : n;
  })();
  const [search, setSearch] = useState("");
  const [debouncedQuery, setDebouncedQuery] = useState("");
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Clear timer on unmount
  useEffect(() => {
    return () => {
      if (timerRef.current) clearTimeout(timerRef.current);
    };
  }, []);

  const handleSearchChange = useCallback(
    (e: React.ChangeEvent<HTMLInputElement>) => {
      const value = e.target.value;
      setSearch(value);
      if (timerRef.current) clearTimeout(timerRef.current);
      timerRef.current = setTimeout(() => {
        setDebouncedQuery(value);
      }, 300);
    },
    []
  );

  const handleSelectArtist = useCallback((artistId: number) => {
    setSearchParams({ artist: String(artistId) });
    setSearch("");
    setDebouncedQuery("");
  }, [setSearchParams]);

  const handleBack = useCallback(() => {
    if (selectedAlbumId !== null) {
      if (selectedArtistId === null) return;
      setSearchParams({ artist: String(selectedArtistId) });
    } else {
      setSearchParams({});
    }
  }, [selectedAlbumId, selectedArtistId, setSearchParams]);

  const handleSelectAlbum = useCallback((albumId: number) => {
    if (selectedArtistId === null) return;
    setSearchParams({ artist: String(selectedArtistId), album: String(albumId) });
  }, [selectedArtistId, setSearchParams]);

  // Artist list (list view)
  const artists = useLibraryArtists(
    selectedArtistId === null ? (debouncedQuery || undefined) : undefined
  );

  // Artist detail (detail view)
  const artist = useLibraryArtist(selectedArtistId);
  const artistAlbums = useLibraryArtistAlbums(selectedArtistId);
  const artistTracks = useLibraryArtistTracks(selectedArtistId);

  // Selected album. Prefer the loaded grid record: on the click-from-grid path
  // the album is already there, so the by-id query is skipped entirely. The
  // dedicated by-id query only fires when the grid has NOT produced the album —
  // that is what lets a deep link / reload to an album past the first loaded
  // page resolve independently of pagination.
  const gridAlbum = useMemo(
    () => (artistAlbums.albums ?? []).find((a) => a.id === selectedAlbumId) ?? null,
    [artistAlbums.albums, selectedAlbumId]
  );
  const selectedAlbumQuery = useLibraryAlbum(selectedAlbumId, gridAlbum != null);
  const selectedAlbum = selectedAlbumQuery.data ?? gridAlbum;

  return (
    <div className="flex flex-col gap-0">
      {/* Header */}
      <div className="mb-6 flex flex-wrap items-center justify-between gap-3">
        <h1 className="text-xl font-bold text-white">Library</h1>
      </div>

      {/* Search bar — only in list view */}
      {selectedArtistId === null && (
        <div className="relative mb-6">
          <Search
            size={18}
            className="absolute left-3 top-1/2 -translate-y-1/2 text-slate-500"
          />
          <input
            type="text"
            value={search}
            onChange={handleSearchChange}
            placeholder="Search artists…"
            className="w-full rounded-lg border border-slate-800 bg-slate-900 py-2.5 pl-10 pr-4 text-sm text-white placeholder:text-slate-500 transition-colors focus:border-purple-500 focus:outline-none focus:ring-1 focus:ring-purple-500"
          />
        </div>
      )}

      {/* Artist list view */}
      {selectedArtistId === null && (
        <ArtistsSection
          artists={artists.artists}
          isLoading={artists.isLoading}
          isError={artists.isError}
          error={artists.error}
          hasNextPage={artists.hasNextPage}
          isFetchingNextPage={artists.isFetchingNextPage}
          onLoadMore={artists.fetchNextPage}
          onSelectArtist={handleSelectArtist}
        />
      )}

      {/* Artist detail view */}
      {selectedArtistId !== null && selectedAlbumId === null && (
        <ArtistDetailView
          artist={artist.data}
          albums={artistAlbums.albums ?? []}
          tracks={artistTracks.data ?? []}
          isLoadingArtist={artist.isLoading}
          isLoadingAlbums={artistAlbums.isLoading}
          isLoadingTracks={artistTracks.isLoading}
          hasNextPage={artistAlbums.hasNextPage}
          isFetchingNextPage={artistAlbums.isFetchingNextPage}
          onLoadMore={artistAlbums.fetchNextPage}
          onBack={handleBack}
          onSelectAlbum={handleSelectAlbum}
        />
      )}

      {/* Album detail view — resolved by id, falling back to the loaded grid */}
      {selectedArtistId !== null && selectedAlbumId !== null && (
        selectedAlbum ? (
          <AlbumDetailView
            album={selectedAlbum}
            artistName={artist.data?.name ?? ""}
            onBack={handleBack}
          />
        ) : selectedAlbumQuery.isError ? (
          isNotFoundError(selectedAlbumQuery.error) ? (
            <StatusMessage variant="error" message="Album not found." />
          ) : (
            <StatusMessage variant="error" message="Couldn't load this album.">
              <button
                type="button"
                onClick={() => void selectedAlbumQuery.refetch()}
                className="mt-2 rounded border border-red-800 px-3 py-1 text-sm text-red-200 transition-colors hover:bg-red-900/40"
              >
                Retry
              </button>
            </StatusMessage>
          )
        ) : selectedAlbumQuery.isLoading ? (
          <div className="flex items-center justify-center py-12">
            <Spinner size="md" />
          </div>
        ) : (
          <StatusMessage variant="error" message="Album not found." />
        )
      )}
    </div>
  );
}
