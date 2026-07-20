import { useState, type FC } from "react";
import { RefreshCw, Download, Trash2, Music, Check, X, ArrowDown, Loader2, Clock, ChevronDown } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { toast } from "sonner";
import type { Playlist, PlaylistTrackDownloadStatus } from "../../api/types";
import { useSyncPlaylist, useDownloadMissing, usePlaylist, useDeletePlaylist } from "../../hooks/use-playlists";
import Button from "../../components/Button";
import Badge from "../../components/Badge";
import Spinner from "../../components/Spinner";

interface PlaylistCardProps {
  playlist: Playlist;
}

const STATUS_BADGE: Record<
  PlaylistTrackDownloadStatus,
  { variant: "success" | "warning" | "error" | "muted"; icon: LucideIcon; label: string }
> = {
  linked: { variant: "success", icon: Check, label: "Linked" },
  downloading: { variant: "warning", icon: Loader2, label: "Downloading" },
  queued: { variant: "muted", icon: Clock, label: "Queued" },
  unmatched: { variant: "muted", icon: ArrowDown, label: "Unmatched" },
};

const PlaylistCard: FC<PlaylistCardProps> = ({ playlist }) => {
  const [expanded, setExpanded] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);

  const syncMutation = useSyncPlaylist();
  const downloadMissingMutation = useDownloadMissing();
  const deleteMutation = useDeletePlaylist();

  const { data: trackData, isLoading: tracksLoading } = usePlaylist(
    expanded ? playlist.id : 0,
  );

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

  const handleDelete = (e: React.MouseEvent) => {
    e.stopPropagation();
    if (!confirmDelete) {
      setConfirmDelete(true);
      return;
    }
    deleteMutation.mutate(playlist.id, {
      onSuccess: () => {
        toast.success("Playlist removed");
        setConfirmDelete(false);
        setExpanded(false);
      },
      onError: (err) =>
        toast.error(err instanceof Error ? err.message : "Failed to remove playlist"),
    });
  };

  const tracks = trackData?.tracks ?? [];

  return (
    <div className="rounded-lg border border-slate-800 bg-slate-900">
      {/* ── Card header (always visible) ── */}
      <div
        onClick={() => {
          setExpanded((v) => !v);
          setConfirmDelete(false);
        }}
        className="flex cursor-pointer items-center gap-4 p-4 transition-colors hover:bg-slate-800/80"
        role="button"
        tabIndex={0}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === " ") {
            setExpanded((v) => !v);
            setConfirmDelete(false);
          }
        }}
      >
        {/* Cover art */}
        <div className="flex h-12 w-12 shrink-0 items-center justify-center overflow-hidden rounded-md bg-slate-800">
          {playlist.cover_url ? (
            <img src={playlist.cover_url} alt="" className="h-full w-full object-cover" />
          ) : (
            <Music size={20} className="text-slate-500" />
          )}
        </div>

        {/* Info */}
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <h4 className="truncate text-sm font-semibold text-white">{playlist.name}</h4>
            <Badge variant="muted">{playlist.source}</Badge>
          </div>
          <div className="mt-1 flex items-center gap-2">
            <span className="text-xs text-slate-400">{playlist.track_count} tracks</span>
            {isSynced ? (
              <Badge variant="success"><Check size={10} className="mr-0.5" />Synced</Badge>
            ) : (
              <Badge variant="warning">New</Badge>
            )}
          </div>
        </div>

        {/* Action buttons */}
        <div className="flex shrink-0 items-center gap-1">
          <Button variant="ghost" size="sm" loading={syncMutation.isPending} onClick={handleSync} title="Sync playlist with source">
            <RefreshCw size={14} />
          </Button>
          <Button variant="ghost" size="sm" loading={downloadMissingMutation.isPending} onClick={handleDownloadMissing} title="Download missing tracks">
            <Download size={14} />
          </Button>
          <Button variant="ghost" size="sm" onClick={handleDelete} title="Remove playlist" loading={confirmDelete && deleteMutation.isPending}>
            <Trash2 size={14} className={confirmDelete ? "text-red-500" : "text-red-400"} />
          </Button>
          <ChevronDown size={16} className={`text-slate-500 transition-transform ${expanded ? "rotate-180" : ""}`} />
        </div>
      </div>

      {/* ── Expanded track list ── */}
      {expanded && (
        <div className="border-t border-slate-800">
          {/* Confirm delete banner */}
          {confirmDelete && (
            <div className="flex items-center justify-between border-b border-red-900/50 bg-red-950/30 px-4 py-2">
              <span className="text-sm text-red-300">Remove "{playlist.name}"? This cannot be undone.</span>
              <div className="flex gap-2">
                <Button variant="ghost" size="sm" onClick={(e) => { e.stopPropagation(); setConfirmDelete(false); }}>
                  Cancel
                </Button>
                <Button variant="danger" size="sm" loading={deleteMutation.isPending} onClick={handleDelete}>
                  Remove
                </Button>
              </div>
            </div>
          )}

          {/* Track list content */}
          <div className="max-h-96 overflow-y-auto p-4">
            {tracksLoading ? (
              <div className="flex justify-center py-8"><Spinner size="lg" /></div>
            ) : tracks.length === 0 ? (
              <p className="py-8 text-center text-slate-500">No tracks found.</p>
            ) : (
              <ul className="space-y-2">
                {tracks.map((track, idx) => {
                  const status = track.download_status ?? "unmatched";
                  const badge = STATUS_BADGE[status];
                  const Icon = badge.icon;
                  return (
                    <li
                      key={`${track.source_track_id}-${idx}`}
                      className="flex items-center gap-3 rounded-md border border-slate-800 bg-slate-950 px-3 py-2"
                    >
                      <span className="w-6 shrink-0 text-right text-xs text-slate-500">{track.position}</span>
                      <div className="min-w-0 flex-1">
                        <p className="truncate text-sm text-white">{track.title}</p>
                        <p className="truncate text-xs text-slate-400">{track.artist}</p>
                      </div>
                      <Badge variant={badge.variant}>
                        <Icon size={12} className={`mr-1 ${status === "downloading" ? "animate-spin" : ""}`} />
                        {badge.label}
                      </Badge>
                    </li>
                  );
                })}
              </ul>
            )}
          </div>
        </div>
      )}
    </div>
  );
};

export default PlaylistCard;
