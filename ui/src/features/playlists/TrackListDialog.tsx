import type { FC } from "react";
import * as Dialog from "@radix-ui/react-dialog";
import { X, Check, ArrowDown } from "lucide-react";
import { toast } from "sonner";
import { usePlaylist, useDeletePlaylist } from "../../hooks/use-playlists";
import Button from "../../components/Button";
import Spinner from "../../components/Spinner";
import Badge from "../../components/Badge";

interface TrackListDialogProps {
  /** Numeric playlist ID; dialog fetches detail when open and ID is valid. */
  playlistId: number | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** When true, shows a danger "Remove Playlist" button at the bottom. */
  confirmRemove?: boolean;
}

const TrackListDialog: FC<TrackListDialogProps> = ({
  playlistId,
  open,
  onOpenChange,
  confirmRemove = false,
}) => {
  const enabled = open && playlistId !== null && playlistId > 0;
  const { data, isLoading } = usePlaylist(playlistId ?? 0);
  const deleteMutation = useDeletePlaylist();

  const handleRemove = () => {
    if (!playlistId) return;
    deleteMutation.mutate(playlistId, {
      onSuccess: () => {
        toast.success("Playlist removed");
        onOpenChange(false);
      },
      onError: (err) => {
        toast.error(
          err instanceof Error ? err.message : "Failed to remove playlist",
        );
      },
    });
  };

  const playlist = data?.playlist;
  const tracks = data?.tracks ?? [];

  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-black/60" />
        <Dialog.Content className="fixed left-1/2 top-1/2 z-50 flex max-h-[80vh] w-full max-w-lg -translate-x-1/2 -translate-y-1/2 flex-col rounded-lg border border-slate-800 bg-slate-900">
          {/* Header */}
          <div className="flex shrink-0 items-center justify-between border-b border-slate-800 p-4">
            <Dialog.Title className="truncate text-lg font-semibold text-white">
              {playlist?.name ?? "Playlist Tracks"}
            </Dialog.Title>
            <Dialog.Close
              aria-label="Close"
              className="rounded p-1 text-slate-400 hover:bg-slate-800 hover:text-white"
            >
              <X size={18} />
            </Dialog.Close>
          </div>

          {/* Track list */}
          <div className="flex-1 overflow-y-auto p-4">
            {isLoading ? (
              <div className="flex justify-center py-8">
                <Spinner size="lg" />
              </div>
            ) : tracks.length === 0 ? (
              <p className="py-8 text-center text-slate-500">
                No tracks found.
              </p>
            ) : (
              <ul className="space-y-2">
                {tracks.map((track, idx) => (
                  <li
                    key={`${track.source_track_id}-${idx}`}
                    className="flex items-center gap-3 rounded-md border border-slate-800 bg-slate-950 px-3 py-2"
                  >
                    <span className="w-6 shrink-0 text-right text-xs text-slate-500">
                      {track.position}
                    </span>
                    <div className="min-w-0 flex-1">
                      <p className="truncate text-sm text-white">
                        {track.title}
                      </p>
                      <p className="truncate text-xs text-slate-400">
                        {track.artist}
                      </p>
                    </div>
                    {track.linked ? (
                      <Badge variant="success">
                        <Check size={12} className="mr-1" />
                        Linked
                      </Badge>
                    ) : (
                      <span
                        className="text-yellow-500"
                        title="Unmatched — no library match"
                      >
                        <ArrowDown size={14} />
                      </span>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </div>

          {/* Remove button (only in confirm-remove mode) */}
          {confirmRemove && playlistId && (
            <div className="shrink-0 border-t border-slate-800 p-4">
              <Button
                variant="danger"
                size="default"
                loading={deleteMutation.isPending}
                onClick={handleRemove}
                className="w-full"
              >
                Remove Playlist
              </Button>
            </div>
          )}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
};

export default TrackListDialog;
