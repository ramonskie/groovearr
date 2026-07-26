import {
  useState,
  useCallback,
  type FormEvent,
} from "react";
import { useNavigate } from "react-router-dom";
import { toast } from "sonner";
import {
  useDiscoveryProviders,
  useDiscoverySearch,
  useArtistAlbums,
  useAlbumTracks,
  useDownloadAlbum,
} from "../../hooks/use-discovery";
import Button from "../../components/Button";
import type { ArtistSummary, DiscoveryAlbum, DiscoveryTrack } from "../../api/types";

type View = "search" | "artist" | "album";

export default function DiscoverPage() {
  const navigate = useNavigate();
  const [query, setQuery] = useState("");
  const [view, setView] = useState<View>("search");
  const [selectedArtistId, setSelectedArtistId] = useState<string | null>(null);
  const [selectedArtistProvider, setSelectedArtistProvider] = useState("");
  const [selectedArtistName, setSelectedArtistName] = useState("");
  const [selectedAlbumId, setSelectedAlbumId] = useState<string | null>(null);
  const [selectedAlbumProvider, setSelectedAlbumProvider] = useState("");
  const [selectedAlbumName, setSelectedAlbumName] = useState("");
  const [coverUrl, setCoverUrl] = useState("");

  const { data: providers } = useDiscoveryProviders();
  const searchMutation = useDiscoverySearch();
  const { data: albums } = useArtistAlbums(selectedArtistId, selectedArtistProvider);
  const { data: tracks } = useAlbumTracks(selectedAlbumId, selectedAlbumProvider);
  const downloadAlbumMutation = useDownloadAlbum();

  const noProviders = providers && providers.length === 0;

  const handleSearch = useCallback(
    (e: FormEvent) => {
      e.preventDefault();
      if (!query.trim()) return;
      setView("search");
      searchMutation.mutate({ query: query.trim() });
    },
    [query, searchMutation],
  );

  const handleArtistClick = useCallback((artist: ArtistSummary) => {
    setSelectedArtistId(artist.provider_id);
    setSelectedArtistProvider(artist.provider_name ?? "");
    setSelectedArtistName(artist.name);
    setView("artist");
  }, []);

  const handleAlbumClick = useCallback((album: DiscoveryAlbum) => {
    setSelectedAlbumId(album.provider_id);
    setSelectedAlbumProvider(album.provider_name);
    setSelectedAlbumName(album.title);
    setCoverUrl(album.cover_url ?? "");
    setView("album");
  }, []);

  const handleBack = useCallback(() => {
    if (view === "album") {
      setView("artist");
      setSelectedAlbumId(null);
      setSelectedAlbumProvider("");
    } else if (view === "artist") {
      setView("search");
      setSelectedArtistId(null);
      setSelectedArtistProvider("");
    }
  }, [view]);

  const handleDownloadAlbum = useCallback(() => {
    if (!selectedAlbumId) return;
    downloadAlbumMutation.mutate(selectedAlbumId, {
      onSuccess: (data) => {
        toast.success(`${data.queued}/${data.total} tracks queued`);
        if (data.errors.length > 0) {
          data.errors.slice(0, 3).forEach((e: string) => toast.error(e));
        }
        navigate("/downloads");
      },
      onError: (err: Error) => {
        toast.error(err.message);
      },
    });
  }, [selectedAlbumId, downloadAlbumMutation, navigate]);

  const results = searchMutation.data;

  return (
    <div className="max-w-4xl mx-auto">
      <h1 className="text-2xl font-bold mb-6 text-gray-100">Discover</h1>

      {/* Search bar */}
      <form onSubmit={handleSearch} className="flex gap-3 mb-6">
        <input
          type="text"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search artists or albums..."
          className="flex-1 px-4 py-2 rounded-lg bg-gray-800 border border-gray-700 text-gray-100 placeholder-gray-500 focus:outline-none focus:border-purple-500"
        />
        <Button type="submit" disabled={searchMutation.isPending}>
          {searchMutation.isPending ? "Searching..." : "Search"}
        </Button>
      </form>

      {/* Back button */}
      {view !== "search" && (
        <button
          onClick={handleBack}
          className="mb-4 text-purple-400 hover:text-purple-300 text-sm flex items-center gap-1"
        >
          ← Back to {view === "album" ? selectedArtistName : "search results"}
        </button>
      )}

      {/* No providers configured */}
      {noProviders && (
        <div className="text-center text-gray-500 py-12">
          No discovery providers available. This should never happen —
          Deezer's free public API is always available.
        </div>
      )}

      {/* Loading / Error / Empty states */}
      {searchMutation.isPending && (
        <div className="text-center text-gray-400 py-12">Searching...</div>
      )}
      {searchMutation.isError && (
        <div className="rounded-lg border border-red-800 bg-red-950/50 text-red-300 p-4 text-sm">
          {searchMutation.error?.message ?? "Search failed"}
        </div>
      )}

      {/* Artist view */}
      {view === "search" && results && (
        <div className="space-y-8">
          {results.artists && results.artists.length > 0 && (
            <section>
              <h2 className="text-lg font-semibold text-gray-200 mb-4">
                Artists
              </h2>
              <div className="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-4 gap-4">
                {results.artists.map((artist) => (
                  <button
                    key={artist.provider_id}
                    onClick={() => handleArtistClick(artist)}
                    className="flex flex-col items-center p-4 rounded-lg bg-gray-800/50 hover:bg-gray-800 border border-gray-700/50 hover:border-purple-500/50 transition-colors text-left"
                  >
                    {artist.image_url ? (
                      <img
                        src={artist.image_url}
                        alt={artist.name}
                        className="w-24 h-24 rounded-full object-cover mb-3"
                      />
                    ) : (
                      <div className="w-24 h-24 rounded-full bg-gray-700 mb-3 flex items-center justify-center text-gray-500 text-2xl">
                        ♪
                      </div>
                    )}
                    <span className="text-gray-200 text-sm font-medium text-center truncate w-full">
                      {artist.name}
                    </span>
                    {artist.genres && artist.genres.length > 0 && (
                      <span className="text-gray-500 text-xs mt-1">
                        {artist.genres[0]}
                      </span>
                    )}
                  </button>
                ))}
              </div>
            </section>
          )}

          {results.albums && results.albums.length > 0 && (
            <section>
              <h2 className="text-lg font-semibold text-gray-200 mb-4">
                Albums
              </h2>
              <AlbumGrid
                albums={results.albums}
                onAlbumClick={handleAlbumClick}
              />
            </section>
          )}

          {results.artists?.length === 0 &&
            results.albums?.length === 0 &&
            searchMutation.isSuccess && (
              <div className="text-center text-gray-500 py-12">No results found</div>
            )}
        </div>
      )}

      {/* Artist detail: album grid */}
      {view === "artist" && (
        <section>
          <h2 className="text-xl font-bold text-gray-100 mb-2">
            {selectedArtistName}
          </h2>
          <p className="text-gray-400 mb-6">Albums</p>
          {albums ? (
            <AlbumGrid albums={albums} onAlbumClick={handleAlbumClick} />
          ) : (
            <div className="text-center text-gray-400 py-12">Loading albums...</div>
          )}
        </section>
      )}

      {/* Album detail: tracklist */}
      {view === "album" && (
        <section>
          <div className="flex gap-6 mb-6">
            {coverUrl && (
              <img
                src={coverUrl}
                alt={selectedAlbumName}
                className="w-48 h-48 rounded-lg object-cover shadow-lg"
              />
            )}
            <div className="flex flex-col justify-end">
              <h2 className="text-2xl font-bold text-gray-100">
                {selectedAlbumName}
              </h2>
              <p className="text-gray-400">{selectedArtistName}</p>
              <p className="text-gray-500 text-sm mt-1">
                {tracks?.length ?? "?"} tracks
              </p>
              <div className="mt-4">
                <Button
                  onClick={handleDownloadAlbum}
                  disabled={downloadAlbumMutation.isPending}
                >
                  {downloadAlbumMutation.isPending
                    ? "Downloading..."
                    : "Download Album"}
                </Button>
              </div>
            </div>
          </div>

          {tracks ? (
            <div className="space-y-1">
              {tracks.map((track, i) => (
                <TrackRow key={track.provider_id} track={track} index={i} />
              ))}
            </div>
          ) : (
            <div className="text-center text-gray-400 py-12">Loading tracks...</div>
          )}
        </section>
      )}
    </div>
  );
}

function AlbumGrid({
  albums,
  onAlbumClick,
}: {
  albums: DiscoveryAlbum[];
  onAlbumClick: (album: DiscoveryAlbum) => void;
}) {
  return (
    <div className="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-4 gap-4">
      {albums.map((album) => (
        <button
          key={album.provider_id}
          onClick={() => onAlbumClick(album)}
          className="flex flex-col p-3 rounded-lg bg-gray-800/50 hover:bg-gray-800 border border-gray-700/50 hover:border-purple-500/50 transition-colors text-left"
        >
          {album.cover_url ? (
            <img
              src={album.cover_url}
              alt={album.title}
              className="w-full aspect-square object-cover rounded-md mb-2"
            />
          ) : (
            <div className="w-full aspect-square bg-gray-700 rounded-md mb-2 flex items-center justify-center text-gray-500 text-3xl">
              ♪
            </div>
          )}
          <span className="text-gray-200 text-sm font-medium truncate">
            {album.title}
          </span>
          <span className="text-gray-500 text-xs">
            {album.year ? `${album.year} · ` : ""}
            {album.type}
          </span>
        </button>
      ))}
    </div>
  );
}

function TrackRow({
  track,
  index,
}: {
  track: DiscoveryTrack;
  index: number;
}) {
  const mins = Math.floor(track.duration_ms / 60000);
  const secs = Math.floor((track.duration_ms % 60000) / 1000);
  const duration = `${mins}:${secs.toString().padStart(2, "0")}`;

  return (
    <div className="flex items-center gap-3 px-3 py-2 rounded hover:bg-gray-800/50 text-sm">
      <span className="text-gray-500 w-6 text-right">{index + 1}</span>
      <div className="flex-1 min-w-0">
        <div className="text-gray-200 truncate">{track.title}</div>
        <div className="text-gray-500 text-xs truncate">{track.artist_name}</div>
      </div>
      <span className="text-gray-500 tabular-nums">{duration}</span>
    </div>
  );
}
