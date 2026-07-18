import type { FC } from "react";
import { RefreshCw, Download, Trash2, Music, Check } from "lucide-react";
import { toast } from "sonner";
import type { Playlist } from "../../api/types";
import { useSyncPlaylist, useDownloadMissing } from "../../hooks/use-playlists";
import Button from "../../components/Button";
import Badge from "../../components/Badge";

interface PlaylistCardProps {
  playlist: Playlist;
  /** Fired when the card body is clicked (opens track list). */
  onClick: () => void;
  /** Fired when the Remove action button is clicked. */
  onRemove: () => void;
}

const PlaylistCard: FC<PlaylistCardProps> = ({ playlist, onClick, onRemove }) => {
  const syncMutation = useSyncPlaylist();
  const downloadMissingMutation = useDownloadMissing();

  const isSynced = !!playlist.synced_at;

  const handleSync = (e: React.MouseEvent) => {
    e.stopPropagation();
    syncMutation.mutate(playlist.id, {
      onSuccess: () => toast.success(`Syncing "${playlist.name}"`),
      onError: (err) =>
        toast.error(err instanceof Error ? err.message : "Sync failed"),
    });
  };

  const handleDownloadMissing = (e: React.MouseEvent) => {
    e.stopPropagation();
    downloadMissingMutation.mutate(playlist.id, {
      onSuccess: (data) =>
        toast.success(`${data.queued} tracks queued for download`),
      onError: (err) =>
        toast.error(err instanceof Error ? err.message : "Download failed"),
    });
  };

  const handleRemove = (e: React.MouseEvent) => {
    e.stopPropagation();
    onRemove();
  };

  return (
    <div
      onClick={onClick}
      className="flex cursor-pointer items-center gap-4 rounded-lg border border-slate-800 bg-slate-900 p-4 transition-colors hover:border-slate-700 hover:bg-slate-800/80"
      role="button"
      tabIndex={0}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") onClick();
      }}
    >
      {/* Cover art or placeholder */}
      <div className="flex h-12 w-12 shrink-0 items-center justify-center overflow-hidden rounded-md bg-slate-800">
        {playlist.cover_url ? (
          <img
            src={playlist.cover_url}
            alt=""
            className="h-full w-full object-cover"
          />
        ) : (
          <Music size={20} className="text-slate-500" />
        )}
      </div>

      {/* Info */}
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <h4 className="truncate text-sm font-semibold text-white">
            {playlist.name}
          </h4>
          <Badge variant="muted">{playlist.source}</Badge>
        </div>
        <div className="mt-1 flex items-center gap-2">
          <span className="text-xs text-slate-400">
            {playlist.track_count} tracks
          </span>
          {isSynced ? (
            <Badge variant="success">
              <Check size={10} className="mr-0.5" />
              Synced
            </Badge>
          ) : (
            <Badge variant="warning">New</Badge>
          )}
        </div>
      </div>

      {/* Action buttons */}
      <div className="flex shrink-0 items-center gap-1">
        <Button
          variant="ghost"
          size="sm"
          loading={syncMutation.isPending}
          onClick={handleSync}
          title="Sync playlist with source"
        >
          <RefreshCw size={14} />
        </Button>
        <Button
          variant="ghost"
          size="sm"
          loading={downloadMissingMutation.isPending}
          onClick={handleDownloadMissing}
          title="Download missing tracks"
        >
          <Download size={14} />
        </Button>
        <Button
          variant="ghost"
          size="sm"
          onClick={handleRemove}
          title="Remove playlist"
        >
          <Trash2 size={14} className="text-red-400" />
        </Button>
      </div>
    </div>
  );
};

export default PlaylistCard;
