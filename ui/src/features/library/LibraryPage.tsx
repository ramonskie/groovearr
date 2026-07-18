import { useState, useRef, useEffect, useCallback } from "react";
import { Search } from "lucide-react";
import {
  useLibraryTracks,
  useLibraryArtists,
  useLibraryAlbums,
} from "../../hooks/use-library";
import ArtistsSection from "./ArtistsSection";
import AlbumGrid from "./AlbumGrid";
import TracksTable from "./TracksTable";
import ScanButton from "./ScanButton";

export default function LibraryPage() {
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

  // Fetch in parallel — separate queries
  const tracks = useLibraryTracks(debouncedQuery || undefined);
  const artists = useLibraryArtists(debouncedQuery || undefined);
  const albums = useLibraryAlbums(debouncedQuery || undefined);

  return (
    <div className="flex flex-col gap-0">
      {/* Header */}
      <div className="mb-6 flex flex-wrap items-center justify-between gap-3">
        <h1 className="text-xl font-bold text-white">Library</h1>
        <ScanButton />
      </div>

      {/* Search bar */}
      <div className="relative mb-6">
        <Search
          size={18}
          className="absolute left-3 top-1/2 -translate-y-1/2 text-slate-500"
        />
        <input
          type="text"
          value={search}
          onChange={handleSearchChange}
          placeholder="Search artists, albums, tracks…"
          className="w-full rounded-lg border border-slate-800 bg-slate-900 py-2.5 pl-10 pr-4 text-sm text-white placeholder:text-slate-500 focus:border-purple-500 focus:outline-none focus:ring-1 focus:ring-purple-500 transition-colors"
        />
      </div>

      {/* Artists section */}
      <ArtistsSection
        artists={artists.data ?? []}
        isLoading={artists.isLoading}
        isError={artists.isError}
        error={artists.error}
      />

      {/* Albums grid */}
      <AlbumGrid
        albums={albums.data ?? []}
        artists={artists.data ?? []}
        isLoading={albums.isLoading}
        isError={albums.isError}
        error={albums.error}
      />

      {/* Tracks table */}
      <TracksTable
        tracks={tracks.data ?? []}
        isLoading={tracks.isLoading}
        isError={tracks.isError}
        error={tracks.error}
        hasSearch={debouncedQuery.length > 0}
      />
    </div>
  );
}
