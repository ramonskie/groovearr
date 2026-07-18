import Spinner from "../../components/Spinner";
import StatusMessage from "../../components/StatusMessage";
import type { Artist } from "../../api/types";

interface ArtistsSectionProps {
  artists: Artist[];
  isLoading: boolean;
  isError: boolean;
  error: unknown;
}

export default function ArtistsSection({
  artists,
  isLoading,
  isError,
  error,
}: ArtistsSectionProps) {
  if (isLoading) {
    return (
      <section className="mb-4">
        <h2 className="mb-3 text-sm font-semibold uppercase tracking-wider text-slate-400">
          Artists
        </h2>
        <div className="flex items-center justify-center py-4">
          <Spinner size="sm" />
        </div>
      </section>
    );
  }

  if (isError) {
    return (
      <section className="mb-4">
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

  if (artists.length === 0) return null;

  return (
    <section className="mb-4">
      <h2 className="mb-3 text-sm font-semibold uppercase tracking-wider text-slate-400">
        Artists
      </h2>
      <div className="flex flex-wrap gap-2">
        {artists.map((artist) => (
          <span
            key={artist.id}
            className="rounded bg-slate-800 px-3 py-1 text-xs text-slate-300 transition-colors hover:bg-slate-700 hover:text-white cursor-default"
          >
            {artist.name}
          </span>
        ))}
      </div>
    </section>
  );
}
