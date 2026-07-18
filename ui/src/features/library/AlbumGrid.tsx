import Spinner from "../../components/Spinner";
import StatusMessage from "../../components/StatusMessage";
import AlbumCard from "./AlbumCard";
import type { Album, Artist } from "../../api/types";

interface AlbumGridProps {
  albums: Album[];
  artists: Artist[];
  isLoading: boolean;
  isError: boolean;
  error: unknown;
}

export default function AlbumGrid({
  albums,
  artists,
  isLoading,
  isError,
  error,
}: AlbumGridProps) {
  // Build artist name lookup
  const artistMap = new Map<number, string>();
  for (const a of artists) {
    artistMap.set(a.id, a.name);
  }

  if (isLoading) {
    return (
      <section className="mb-6">
        <h2 className="mb-3 text-sm font-semibold uppercase tracking-wider text-slate-400">
          Albums
        </h2>
        <div className="flex items-center justify-center py-12">
          <Spinner size="md" />
        </div>
      </section>
    );
  }

  if (isError) {
    return (
      <section className="mb-6">
        <h2 className="mb-3 text-sm font-semibold uppercase tracking-wider text-slate-400">
          Albums
        </h2>
        <StatusMessage
          variant="error"
          message={
            error instanceof Error ? error.message : "Failed to load albums."
          }
        />
      </section>
    );
  }

  if (albums.length === 0) return null;

  return (
    <section className="mb-6">
      <h2 className="mb-3 text-sm font-semibold uppercase tracking-wider text-slate-400">
        Albums
      </h2>
      <div className="grid grid-cols-[repeat(auto-fill,minmax(160px,1fr))] gap-4">
        {albums.map((album) => (
          <AlbumCard
            key={album.id}
            album={album}
            artistName={artistMap.get(album.artist_id)}
          />
        ))}
      </div>
    </section>
  );
}
