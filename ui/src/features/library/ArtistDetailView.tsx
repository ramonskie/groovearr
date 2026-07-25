import { useState } from "react";
import { ArrowLeft, Music } from "lucide-react";
import Spinner from "../../components/Spinner";
import StatusMessage from "../../components/StatusMessage";
import type { Artist, Album, Track } from "../../api/types";

interface ArtistDetailViewProps {
  artist: Artist | undefined;
  albums: Album[];
  tracks: Track[];
  isLoadingArtist: boolean;
  isLoadingAlbums: boolean;
  isLoadingTracks: boolean;
  onBack: () => void;
  onSelectAlbum: (albumId: number) => void;
}

/** Build album-title lookup map from albums list. */
function buildAlbumMap(albums: Album[]): Map<number, string> {
  const map = new Map<number, string>();
  for (const a of albums) {
    map.set(a.id, a.title);
  }
  return map;
}

/** Format file size to human-readable string. */
function formatSize(bytes?: number): string {
  if (bytes == null) return "—";
  if (bytes < 1024) return `${bytes} B`;
  const kb = bytes / 1024;
  if (kb < 1024) return `${kb.toFixed(1)} KB`;
  const mb = kb / 1024;
  if (mb < 1024) return `${mb.toFixed(1)} MB`;
  const gb = mb / 1024;
  return `${gb.toFixed(1)} GB`;
}

function AlbumCard({ album, onSelect }: { album: Album; onSelect: (id: number) => void }) {
  const [imgError, setImgError] = useState(false);

  return (
    <button
      type="button"
      onClick={() => onSelect(album.id)}
      className="cursor-pointer rounded-lg border border-slate-800 bg-slate-900 text-left transition-colors hover:border-slate-700"
    >
      <div className="relative aspect-square w-full overflow-hidden rounded-t-lg bg-slate-800">
        {!imgError ? (
          <img
            src={`/api/covers/${album.id}`}
            alt={album.title}
            loading="lazy"
            onError={() => setImgError(true)}
            className="h-full w-full object-cover"
          />
        ) : (
          <div className="flex h-full w-full items-center justify-center">
            <Music size={36} className="text-slate-600" />
          </div>
        )}
      </div>
      <div className="p-3">
        <p className="truncate text-sm font-medium text-white" title={album.title}>
          {album.title}
        </p>
        {album.year && (
          <p className="mt-1 text-[11px] text-slate-500">{album.year}</p>
        )}
      </div>
    </button>
  );
}

export default function ArtistDetailView({
  artist,
  albums,
  tracks,
  isLoadingArtist,
  isLoadingAlbums,
  isLoadingTracks,
  onBack,
  onSelectAlbum,
}: ArtistDetailViewProps) {
  const albumMap = buildAlbumMap(albums);

  return (
    <div className="flex flex-col gap-0">
      {/* Header with back button */}
      <div className="mb-6 flex items-center gap-3">
        <button
          type="button"
          onClick={onBack}
          className="flex h-9 w-9 items-center justify-center rounded-lg border border-slate-800 bg-slate-900 text-slate-400 transition-colors hover:border-slate-700 hover:text-white"
          title="Back to artists"
        >
          <ArrowLeft size={18} />
        </button>
        {isLoadingArtist ? (
          <div className="flex items-center gap-2">
            <Spinner size="sm" />
            <span className="text-sm text-slate-400">Loading…</span>
          </div>
        ) : artist ? (
          <div>
            <h1 className="text-xl font-bold text-white">{artist.name}</h1>
            {artist.genres && artist.genres.length > 0 && (
              <p className="mt-0.5 text-sm text-slate-400">
                {artist.genres.join(", ")}
              </p>
            )}
          </div>
        ) : (
          <StatusMessage variant="error" message="Artist not found." />
        )}
      </div>

      {/* Albums section */}
      <section className="mb-6">
        <h2 className="mb-3 text-sm font-semibold uppercase tracking-wider text-slate-400">
          Albums
        </h2>
        {isLoadingAlbums ? (
          <div className="flex items-center justify-center py-8">
            <Spinner size="md" />
          </div>
        ) : albums.length === 0 ? (
          <p className="py-8 text-center text-sm text-slate-500">No albums found.</p>
        ) : (
          <div className="grid grid-cols-[repeat(auto-fill,minmax(160px,1fr))] gap-4">
            {albums.map((album) => (
              <AlbumCard key={album.id} album={album} onSelect={onSelectAlbum} />
            ))}
          </div>
        )}
      </section>

      {/* Tracks section */}
      <section>
        <h2 className="mb-3 text-sm font-semibold uppercase tracking-wider text-slate-400">
          Tracks
        </h2>
        {isLoadingTracks ? (
          <div className="flex items-center justify-center py-8">
            <Spinner size="md" />
          </div>
        ) : tracks.length === 0 ? (
          <p className="py-8 text-center text-sm text-slate-500">No tracks found.</p>
        ) : (
          <div className="overflow-x-auto rounded-lg border border-slate-800">
            <table className="w-full text-left text-sm">
              <thead>
                <tr className="border-b border-slate-800">
                  <th className="w-10 px-4 py-3 text-xs font-semibold uppercase tracking-wider text-slate-400">
                    #
                  </th>
                  <th className="px-4 py-3 text-xs font-semibold uppercase tracking-wider text-slate-400">
                    Title
                  </th>
                  <th className="hidden px-4 py-3 text-xs font-semibold uppercase tracking-wider text-slate-400 sm:table-cell">
                    Album
                  </th>
                  <th className="w-24 px-4 py-3 text-right text-xs font-semibold uppercase tracking-wider text-slate-400">
                    Size
                  </th>
                </tr>
              </thead>
              <tbody>
                {tracks.map((track, idx) => (
                  <tr
                    key={track.id}
                    className="border-b border-slate-800/50 transition-colors hover:bg-slate-800/30"
                  >
                    <td className="px-4 py-3 text-xs text-slate-500 tabular-nums">
                      {idx + 1}
                    </td>
                    <td className="px-4 py-3 font-medium text-slate-200">
                      <span className="line-clamp-1">{track.title}</span>
                    </td>
                    <td className="hidden px-4 py-3 sm:table-cell">
                      <span className="block max-w-xs truncate text-xs text-slate-500">
                        {albumMap.get(track.album_id) || "—"}
                      </span>
                    </td>
                    <td className="px-4 py-3 text-right text-xs text-slate-500 tabular-nums">
                      {formatSize(track.file_size)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </div>
  );
}
