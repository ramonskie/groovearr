import { useState } from "react";
import Spinner from "../../components/Spinner";
import StatusMessage from "../../components/StatusMessage";
import type { Artist } from "../../api/types";

interface ArtistsSectionProps {
  artists: Artist[];
  isLoading: boolean;
  isError: boolean;
  error: unknown;
  onSelectArtist: (artistId: number) => void;
}

function ArtistCard({
  artist,
  onSelect,
}: {
  artist: Artist;
  onSelect: (artistId: number) => void;
}) {
  const [imgError, setImgError] = useState(false);
  const letter = artist.name.charAt(0).toUpperCase();
  const hue = (artist.id * 137) % 360;
  const bgColor = `hsl(${hue}, 45%, 18%)`;
  const textColor = `hsl(${hue}, 60%, 70%)`;

  return (
    <button
      type="button"
      onClick={() => onSelect(artist.id)}
      className="flex cursor-pointer flex-col items-center gap-2 rounded-lg p-2 text-center transition-colors hover:bg-slate-800/50"
    >
      {/* Circular artist image or letter avatar */}
      <div className="aspect-square w-full overflow-hidden rounded-full" style={{ backgroundColor: bgColor }}>
        {artist.thumb_url && !imgError ? (
          <img
            src={artist.thumb_url}
            alt={artist.name}
            loading="lazy"
            onError={() => setImgError(true)}
            className="h-full w-full object-cover"
          />
        ) : (
          <div className="flex h-full w-full items-center justify-center">
            <span className="select-none text-3xl font-bold" style={{ color: textColor }}>
              {letter}
            </span>
          </div>
        )}
      </div>

      {/* Name */}
      <p className="w-full truncate text-sm font-medium text-white" title={artist.name}>
        {artist.name}
      </p>
    </button>
  );
}

export default function ArtistsSection({
  artists,
  isLoading,
  isError,
  error,
  onSelectArtist,
}: ArtistsSectionProps) {
  if (isLoading) {
    return (
      <section className="mb-6">
        <h2 className="mb-3 text-sm font-semibold uppercase tracking-wider text-slate-400">
          Artists
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
          Artists
        </h2>
        <StatusMessage
          variant="error"
          message={
            error instanceof Error ? error.message : "Failed to load artists."
          }
        />
      </section>
    );
  }

  if (artists.length === 0) {
    return (
      <section className="mb-6">
        <h2 className="mb-3 text-sm font-semibold uppercase tracking-wider text-slate-400">
          Artists
        </h2>
        <p className="py-12 text-center text-sm text-slate-500">
          No artists in library. Scan your music folder to populate the library.
        </p>
      </section>
    );
  }

  return (
    <section className="mb-6">
      <h2 className="mb-3 text-sm font-semibold uppercase tracking-wider text-slate-400">
        Artists
      </h2>
      <div className="grid grid-cols-[repeat(auto-fill,minmax(160px,1fr))] gap-4">
        {artists.map((artist) => (
          <ArtistCard
            key={artist.id}
            artist={artist}
            onSelect={onSelectArtist}
          />
        ))}
      </div>
    </section>
  );
}
