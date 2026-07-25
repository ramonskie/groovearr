import type { FC } from "react";
import type { DownloadRecord, DownloadState } from "../../api/types";
import Card from "../../components/Card";
import Button from "../../components/Button";
import Badge from "../../components/Badge";
import DownloadProgressBar from "./DownloadProgressBar";

interface DownloadItemProps {
  download: DownloadRecord;
  onCancel: (id: string) => void;
  onRetry: (id: string) => void;
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

type BadgeDef = { variant: "success" | "warning" | "error" | "muted"; label: string };

function resolveBadge(state: DownloadState, retryCount: number | undefined): BadgeDef {
  const rc = retryCount ?? 0;
  switch (state) {
    case "queued":
      return { variant: "muted", label: "Queued" };
    case "downloading":
      return { variant: "muted", label: "Downloading" };
    case "importPending":
      return { variant: "warning", label: "Pending Import" };
    case "importing":
      return { variant: "warning", label: "Importing" };
    case "imported":
      return { variant: "success", label: "Imported" };
    case "failedPending":
      return { variant: "warning", label: `Retrying (${rc}/5)` };
    case "failed":
      return {
        variant: "error",
        label: rc > 0 ? `Failed (retry ${rc}/5)` : "Failed",
      };
    case "ignored":
      return { variant: "muted", label: "Ignored" };
  }
}

const SHOW_CANCEL_STATES: Set<DownloadState> = new Set([
  "queued",
  "downloading",
  "importPending",
  "importing",
  "failedPending",
]);

const SHOW_RETRY_STATES: Set<DownloadState> = new Set([
  "failed",
  "failedPending",
]);

const SHOW_PROGRESS_STATES: Set<DownloadState> = new Set([
  "queued",
  "downloading",
  "importPending",
  "importing",
]);

// ─── Component ──────────────────────────────────────────────────────

const DownloadItem: FC<DownloadItemProps> = ({
  download,
  onCancel,
  onRetry,
  isCancelling,
}) => {
  const badge = resolveBadge(download.state, download.retry_count);
  const showCancel = SHOW_CANCEL_STATES.has(download.state);
  const showRetry = SHOW_RETRY_STATES.has(download.state);
  const showProgress = SHOW_PROGRESS_STATES.has(download.state);

  return (
    <Card className="mb-3">
      <div className="space-y-2">
        {/* Top row: name + action button */}
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
          {showCancel && (
            <Button
              variant="danger"
              size="sm"
              loading={isCancelling}
              onClick={() => onCancel(download.id)}
            >
              Cancel
            </Button>
          )}
          {showRetry && (
            <Button
              variant="primary"
              size="sm"
              loading={isCancelling}
              onClick={() => onRetry(download.id)}
            >
              Retry
            </Button>
          )}
        </div>

        {/* Progress bar */}
        {showProgress && (
          <DownloadProgressBar percentage={download.progress} />
        )}

        {/* Stats row */}
        <div className="flex items-center gap-4 text-xs text-slate-400">
          <span>
            {formatBytes(download.transferred)} / {formatBytes(download.size)}
          </span>
          {download.format && (
            <span className="text-slate-500">
              {download.format.toUpperCase()}
              {download.bitrate && download.bitrate > 0 && ` ${download.bitrate}kbps`}
            </span>
          )}
          {showProgress && download.speed > 0 && (
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
