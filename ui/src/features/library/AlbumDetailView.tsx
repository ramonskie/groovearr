import { useState, useCallback } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, Music, Download, Check } from "lucide-react";
import Spinner from "../../components/Spinner";
import StatusMessage from "../../components/StatusMessage";
import { useStartBestDownload } from "../../hooks/use-downloads";
import { useLibraryAlbumDiscovery, useDownloadMissingForAlbum } from "../../hooks/use-library";
import type { Album, DiscoveryTrackEntry } from "../../api/types";

interface AlbumDetailViewProps {
  album: Album;
  artistName: string;
  onBack: () => void;
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

function formatDuration(ms: number): string {
  if (!ms || ms <= 0) return "—";
  const totalSec = Math.floor(ms / 1000);
  const min = Math.floor(totalSec / 60);
  const sec = totalSec % 60;
  return `${min}:${sec.toString().padStart(2, "0")}`;
}

function TrackRow({
  track,
  artistName,
  onDownload,
  isDownloading,
  batchDownloading,
}: {
  track: DiscoveryTrackEntry;
  artistName: string;
  onDownload: (track: DiscoveryTrackEntry) => void;
  isDownloading: boolean;
  batchDownloading: boolean;
}) {
  return (
    <tr className="border-b border-slate-800/50 transition-colors hover:bg-slate-800/30">
      <td className="w-10 px-4 py-3 text-xs text-slate-500 tabular-nums">
        {track.track_number || "—"}
      </td>
      <td className="px-4 py-3">
        <span className="font-medium text-slate-200 line-clamp-1">
          {track.title}
        </span>
      </td>
      <td className="hidden w-16 px-4 py-3 text-right text-xs text-slate-500 tabular-nums sm:table-cell">
        {formatDuration(track.duration_ms)}
      </td>
      <td className="hidden w-20 px-4 py-3 text-right text-xs text-slate-500 tabular-nums sm:table-cell">
        {track.downloaded ? formatSize(track.file_size) : "—"}
      </td>
      <td className="hidden w-16 px-4 py-3 text-right text-xs text-slate-500 tabular-nums sm:table-cell">
        {track.downloaded && track.bitrate ? `${track.bitrate} kbps` : "—"}
      </td>
      <td className="hidden w-14 px-4 py-3 text-center text-xs sm:table-cell">
        {track.downloaded && track.format ? (
          <span className="rounded bg-slate-800 px-1.5 py-0.5 font-mono text-[11px] text-slate-400">
            {track.format}
          </span>
        ) : (
          <span className="text-slate-600">—</span>
        )}
      </td>
      <td className="w-12 px-4 py-3 text-center">
        {track.downloaded ? (
          <Check size={16} className="mx-auto text-emerald-500" />
        ) : (
          <button
            type="button"
            onClick={() => onDownload(track)}
            disabled={isDownloading || batchDownloading}
            className="flex h-7 w-7 items-center justify-center rounded-md border border-slate-700 bg-slate-800 text-slate-400 transition-colors hover:border-purple-500 hover:text-purple-400 disabled:opacity-50"
            title={`Download ${track.title}`}
          >
            <Download size={14} />
          </button>
        )}
      </td>
    </tr>
  );
}

export default function AlbumDetailView({
  album,
  artistName,
  onBack,
}: AlbumDetailViewProps) {
  const [imgError, setImgError] = useState(false);
  const discovery = useLibraryAlbumDiscovery(album.id);
  const startBestDownload = useStartBestDownload();
  const downloadMissing = useDownloadMissingForAlbum();
  const queryClient = useQueryClient();
  const [downloadingIds, setDownloadingIds] = useState<Set<string>>(new Set());
  const [batchError, setBatchError] = useState<string | null>(null);

  const tracks = discovery.data?.tracks ?? [];
  const undownloadedTracks = tracks.filter((t) => !t.downloaded);
  const downloadedCount = tracks.length - undownloadedTracks.length;

  const trackKey = (t: DiscoveryTrackEntry) => `${t.track_number}-${t.title}`;

  const invalidateDiscovery = useCallback(() => {
    queryClient.invalidateQueries({
      queryKey: ["library", "album", album.id, "discovery"],
    });
  }, [album.id, queryClient]);

  const handleDownloadTrack = useCallback((track: DiscoveryTrackEntry) => {
    const key = trackKey(track);
    setDownloadingIds((prev) => new Set(prev).add(key));
    startBestDownload.mutate(
      {
        title: track.title,
        artist: artistName,
        duration: track.duration_ms,
      },
      {
        onSuccess: invalidateDiscovery,
        onSettled: () => {
          setDownloadingIds((prev) => {
            const next = new Set(prev);
            next.delete(key);
            return next;
          });
        },
      }
    );
  }, [artistName, startBestDownload, invalidateDiscovery]);

  const handleDownloadAll = () => {
    setBatchError(null);
    downloadMissing.mutate(album.id, {
      onError: (err) => {
        setBatchError(err.message);
      },
    });
  };

  if (discovery.isError) {
    return (
      <div className="flex flex-col gap-0">
        <div className="mb-6 flex items-center gap-3">
          <button
            type="button"
            onClick={onBack}
            className="flex h-9 w-9 items-center justify-center rounded-lg border border-slate-800 bg-slate-900 text-slate-400 transition-colors hover:border-slate-700 hover:text-white"
            title="Back to artist"
          >
            <ArrowLeft size={18} />
          </button>
        </div>
        <StatusMessage
          variant="error"
          message="Failed to load album tracks."
        />
      </div>
    );
  }

  if (discovery.isLoading) {
    return (
      <div className="flex flex-col gap-0">
        <div className="mb-6 flex items-center gap-3">
          <button
            type="button"
            onClick={onBack}
            className="flex h-9 w-9 items-center justify-center rounded-lg border border-slate-800 bg-slate-900 text-slate-400 transition-colors hover:border-slate-700 hover:text-white"
            title="Back to artist"
          >
            <ArrowLeft size={18} />
          </button>
          <div className="flex items-center gap-2">
            <Spinner size="sm" />
            <span className="text-sm text-slate-400">Loading…</span>
          </div>
        </div>
        <div className="flex items-center justify-center py-12">
          <Spinner size="md" />
        </div>
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-0">
      {/* Header with back button */}
      <div className="mb-6 flex items-center gap-3">
        <button
          type="button"
          onClick={onBack}
          className="flex h-9 w-9 items-center justify-center rounded-lg border border-slate-800 bg-slate-900 text-slate-400 transition-colors hover:border-slate-700 hover:text-white"
          title="Back to artist"
        >
          <ArrowLeft size={18} />
        </button>
        <div>
          <p className="text-sm text-slate-400">{artistName}</p>
          <h1 className="text-xl font-bold text-white">{album.title}</h1>
        </div>
      </div>

      {/* Album info + cover */}
      <div className="mb-6 flex flex-col gap-4 sm:flex-row">
        <div className="h-40 w-40 shrink-0 overflow-hidden rounded-lg bg-slate-800 sm:h-48 sm:w-48">
          {!imgError ? (
            <img
              src={`/api/covers/${album.id}`}
              alt={album.title}
              onError={() => setImgError(true)}
              className="h-full w-full object-cover"
            />
          ) : (
            <div className="flex h-full w-full items-center justify-center">
              <Music size={48} className="text-slate-600" />
            </div>
          )}
        </div>
        <div className="flex flex-col justify-end gap-1">
          <p className="text-xs uppercase tracking-wider text-slate-500">
            {album.album_type || "Album"}
            {album.year ? ` · ${album.year}` : ""}
          </p>
          <p className="text-sm text-slate-400">
            {tracks.length > 0
              ? `${tracks.length} track${tracks.length !== 1 ? "s" : ""}`
              : `${album.track_count} track${album.track_count !== 1 ? "s" : ""}`}
            {downloadedCount > 0 && ` · ${downloadedCount} downloaded`}
          </p>
          {undownloadedTracks.length > 0 && (
            <button
              type="button"
              onClick={handleDownloadAll}
              disabled={downloadMissing.isPending || startBestDownload.isPending}
              className="mt-2 flex w-fit items-center gap-2 rounded-lg bg-purple-600 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-purple-500 disabled:opacity-50"
            >
              <Download size={14} />
              {downloadMissing.isPending
                ? "Queueing…"
                : `Download ${undownloadedTracks.length} missing track${undownloadedTracks.length !== 1 ? "s" : ""}`}
            </button>
          )}
          {batchError && (
            <StatusMessage variant="error" message={batchError} />
          )}
        </div>
      </div>

      {/* Tracks table */}
      <section>
        <h2 className="mb-3 text-sm font-semibold uppercase tracking-wider text-slate-400">
          Tracks
        </h2>
        {tracks.length === 0 ? (
          <p className="py-8 text-center text-sm text-slate-500">
            No tracks available for this album.
          </p>
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
                  <th className="hidden w-16 px-4 py-3 text-right text-xs font-semibold uppercase tracking-wider text-slate-400 sm:table-cell">
                    Duration
                  </th>
                  <th className="hidden w-20 px-4 py-3 text-right text-xs font-semibold uppercase tracking-wider text-slate-400 sm:table-cell">
                    Size
                  </th>
                  <th className="hidden w-16 px-4 py-3 text-right text-xs font-semibold uppercase tracking-wider text-slate-400 sm:table-cell">
                    Bitrate
                  </th>
                  <th className="hidden w-14 px-4 py-3 text-center text-xs font-semibold uppercase tracking-wider text-slate-400 sm:table-cell">
                    Format
                  </th>
                  <th className="w-12 px-4 py-3 text-center text-xs font-semibold uppercase tracking-wider text-slate-400">
                    Status
                  </th>
                </tr>
              </thead>
              <tbody>
                {tracks.map((track) => (
                  <TrackRow
                    key={`${track.track_number}-${track.title}`}
                    track={track}
                    artistName={artistName}
                    onDownload={handleDownloadTrack}
                    isDownloading={downloadingIds.has(trackKey(track))}
                    batchDownloading={downloadMissing.isPending}
                  />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </div>
  );
}
