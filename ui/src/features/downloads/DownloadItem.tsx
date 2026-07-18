import type { FC } from "react";
import type { DownloadRecord, DownloadState } from "../../api/types";
import Card from "../../components/Card";
import Button from "../../components/Button";
import Badge from "../../components/Badge";
import DownloadProgressBar from "./DownloadProgressBar";

interface DownloadItemProps {
  download: DownloadRecord;
  onCancel: (id: string) => void;
  isCancelling: boolean;
}

// ─── Helpers ────────────────────────────────────────────────────────

function formatBytes(bytes: number): string {
  if (bytes === 0) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  const i = Math.floor(Math.log(bytes) / Math.log(1024));
  const val = bytes / 1024 ** i;
  return `${val.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

function formatSpeed(bytesPerSec: number): string {
  return `${formatBytes(bytesPerSec)}/s`;
}

// ─── State display mapping ──────────────────────────────────────────

const STATE_BADGE: Record<
  DownloadState,
  { variant: "success" | "warning" | "error" | "muted"; label: string }
> = {
  initializing: { variant: "muted", label: "Initializing" },
  downloading: { variant: "muted", label: "Downloading" },
  succeeded: { variant: "success", label: "Succeeded" },
  errored: { variant: "error", label: "Errored" },
  cancelled: { variant: "muted", label: "Cancelled" },
  aborted: { variant: "warning", label: "Aborted" },
};

const TERMINAL_STATES: Set<DownloadState> = new Set([
  "succeeded",
  "errored",
  "cancelled",
  "aborted",
]);

// ─── Component ──────────────────────────────────────────────────────

const DownloadItem: FC<DownloadItemProps> = ({
  download,
  onCancel,
  isCancelling,
}) => {
  const badge = STATE_BADGE[download.state];
  const isTerminal = TERMINAL_STATES.has(download.state);

  return (
    <Card className="mb-3">
      <div className="space-y-2">
        {/* Top row: name + cancel button */}
        <div className="flex items-start justify-between gap-2">
          <div className="min-w-0 flex-1">
            <p className="truncate text-sm font-semibold text-white">
              {download.display_name}
            </p>
            <p className="mt-0.5 text-xs">
              <span className="text-purple-400">{download.source_name}</span>
              <span className="mx-1.5 text-slate-600">&middot;</span>
              <Badge variant={badge.variant}>{badge.label}</Badge>
            </p>
          </div>
          {!isTerminal && (
            <Button
              variant="danger"
              size="sm"
              loading={isCancelling}
              onClick={() => onCancel(download.id)}
            >
              Cancel
            </Button>
          )}
        </div>

        {/* Progress bar */}
        <DownloadProgressBar percentage={download.progress} />

        {/* Stats row */}
        <div className="flex items-center gap-4 text-xs text-slate-400">
          <span>
            {formatBytes(download.transferred)} / {formatBytes(download.size)}
          </span>
          {!isTerminal && download.speed > 0 && (
            <span>{formatSpeed(download.speed)}</span>
          )}
        </div>

        {/* Error message */}
        {download.error && (
          <p className="break-all text-xs text-red-400">{download.error}</p>
        )}

        {/* Saved file path */}
        {download.file_path && (
          <p className="break-all text-xs text-green-400">
            {download.file_path}
          </p>
        )}
      </div>
    </Card>
  );
};

export default DownloadItem;
