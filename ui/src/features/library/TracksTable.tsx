import { Download } from "lucide-react";
import Spinner from "../../components/Spinner";
import StatusMessage from "../../components/StatusMessage";
import Badge from "../../components/Badge";
import { libraryTrackDownloadUrl } from "../../api/client";
import type { Track } from "../../api/types";

interface TracksTableProps {
  tracks: Track[];
  isLoading: boolean;
  isError: boolean;
  error: unknown;
  hasSearch: boolean;
}

/** Format file size to human-readable string. **/
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

export default function TracksTable({
  tracks,
  isLoading,
  isError,
  error,
  hasSearch,
}: TracksTableProps) {
  if (isLoading) {
    return (
      <section>
        <h2 className="mb-3 text-sm font-semibold uppercase tracking-wider text-slate-400">
          Tracks
        </h2>
        <div className="flex items-center justify-center py-12">
          <Spinner size="md" />
        </div>
      </section>
    );
  }

  if (isError) {
    return (
      <section>
        <h2 className="mb-3 text-sm font-semibold uppercase tracking-wider text-slate-400">
          Tracks
        </h2>
        <StatusMessage
          variant="error"
          message={
            error instanceof Error ? error.message : "Failed to load tracks."
          }
        />
      </section>
    );
  }

  // Empty state
  if (tracks.length === 0) {
    return (
      <section>
        <h2 className="mb-3 text-sm font-semibold uppercase tracking-wider text-slate-400">
          Tracks
        </h2>
        {hasSearch ? (
          <p className="py-12 text-center text-sm text-slate-500">
            No tracks match your search.
          </p>
        ) : (
          <p className="py-12 text-center text-sm text-slate-500">
            No tracks in library. Downloads are scanned automatically when they
            complete.
          </p>
        )}
      </section>
    );
  }

  return (
    <section>
      <h2 className="mb-3 text-sm font-semibold uppercase tracking-wider text-slate-400">
        Tracks
      </h2>
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
                File Path
              </th>
              <th className="w-24 px-4 py-3 text-right text-xs font-semibold uppercase tracking-wider text-slate-400">
                Size
              </th>
              <th className="w-12 px-4 py-3 text-center text-xs font-semibold uppercase tracking-wider text-slate-400">
                <span className="sr-only">Download</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {tracks.map((track, idx) => (
              <tr
                key={track.id}
                className="border-b border-slate-800/50 hover:bg-slate-800/30 transition-colors"
              >
                <td className="px-4 py-3 text-xs text-slate-500 tabular-nums">
                  {idx + 1}
                </td>
                <td className="px-4 py-3 font-medium text-slate-200">
                  <div className="flex flex-col items-start gap-1">
                    <span className="line-clamp-1">{track.title}</span>
                    {track.added_by_username && (
                      <Badge variant="muted" className="w-fit">
                        Requested by {track.added_by_username}
                      </Badge>
                    )}
                  </div>
                </td>
                <td className="hidden px-4 py-3 sm:table-cell">
                  <span
                    className="block max-w-xs truncate text-xs text-slate-500"
                    title={track.file_path ?? undefined}
                  >
                    {track.file_path || "—"}
                  </span>
                </td>
                <td className="px-4 py-3 text-right text-xs text-slate-500 tabular-nums">
                  {formatSize(track.file_size)}
                </td>
                <td className="px-4 py-3 text-center">
                  <a
                    href={libraryTrackDownloadUrl(track.id)}
                    download
                    title={`Download ${track.title}`}
                    aria-label={`Download ${track.title}`}
                    className="mx-auto flex h-7 w-7 items-center justify-center rounded-md border border-slate-700 bg-slate-800 text-slate-400 transition-colors hover:border-purple-500 hover:text-purple-400"
                  >
                    <Download size={14} />
                  </a>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}
