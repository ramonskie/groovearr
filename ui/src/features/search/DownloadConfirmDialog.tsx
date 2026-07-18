import * as AlertDialog from "@radix-ui/react-alert-dialog";
import type { FC } from "react";
import type { AlbumResult, TrackResult } from "../../api/types";
import Button from "../../components/Button";

interface DownloadConfirmDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  album: AlbumResult;
  /** Called with the album and its tracks when user confirms. */
  onConfirm: (album: AlbumResult, tracks: TrackResult[]) => void;
  loading?: boolean;
}

const DownloadConfirmDialog: FC<DownloadConfirmDialogProps> = ({
  open,
  onOpenChange,
  album,
  onConfirm,
  loading = false,
}) => {
  return (
    <AlertDialog.Root open={open} onOpenChange={onOpenChange}>
      <AlertDialog.Portal>
        <AlertDialog.Overlay className="fixed inset-0 z-50 bg-black/60 data-[state=open]:animate-fadeIn" />
        <AlertDialog.Content className="fixed left-1/2 top-1/2 z-50 w-full max-w-md -translate-x-1/2 -translate-y-1/2 rounded-lg border border-slate-800 bg-slate-900 p-6 shadow-xl data-[state=open]:animate-scaleIn">
          <AlertDialog.Title className="text-lg font-semibold text-white">
            Download Album
          </AlertDialog.Title>

          <AlertDialog.Description className="mt-3 space-y-2 text-sm text-slate-300">
            <p>
              Download{" "}
              <span className="font-medium text-white">
                {album.album_title}
              </span>
              {album.artist && (
                <>
                  {" "}
                  by{" "}
                  <span className="font-medium text-white">
                    {album.artist}
                  </span>
                </>
              )}
              ?
            </p>
            <p>
              {album.track_count} track{album.track_count !== 1 ? "s" : ""}
              {" · "}
              {formatFileSize(album.total_size)}
              {" · "}
              <span className="text-purple-400">{album.dominant_quality}</span>
            </p>
            {album.year && (
              <p className="text-slate-500">Year: {album.year}</p>
            )}
          </AlertDialog.Description>

          <div className="mt-6 flex justify-end gap-3">
            <AlertDialog.Cancel asChild>
              <Button variant="ghost" disabled={loading}>
                Cancel
              </Button>
            </AlertDialog.Cancel>
            <AlertDialog.Action asChild>
              <Button
                variant="primary"
                loading={loading}
                onClick={() => onConfirm(album, album.tracks)}
              >
                Download
              </Button>
            </AlertDialog.Action>
          </div>
        </AlertDialog.Content>
      </AlertDialog.Portal>
    </AlertDialog.Root>
  );
};

/** Format bytes to human-readable string. */
function formatFileSize(bytes: number): string {
  if (bytes === 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  let size = bytes;
  while (size >= 1024 && i < units.length - 1) {
    size /= 1024;
    i++;
  }
  return `${size.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

export default DownloadConfirmDialog;
