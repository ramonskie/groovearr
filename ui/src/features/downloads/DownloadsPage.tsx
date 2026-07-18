import { useEffect, useRef, useState, useCallback } from "react";
import type { DownloadState } from "../../api/types";
import {
  useDownloads,
  useCancelDownload,
  useClearCompleted,
} from "../../hooks/use-downloads";
import { useScanLibrary } from "../../hooks/use-library";
import { useDownloadPollStore } from "../../stores/download-poll";
import { toast } from "sonner";
import Button from "../../components/Button";
import Spinner from "../../components/Spinner";
import StatusMessage from "../../components/StatusMessage";
import DownloadItem from "./DownloadItem";

// ─── Constants ──────────────────────────────────────────────────────

const COMPLETED_STATES = new Set<DownloadState>([
  "succeeded",
  "errored",
  "cancelled",
  "aborted",
]);

// ─── Component ──────────────────────────────────────────────────────

function DownloadsPage() {
  const { data: downloads, isLoading, isError, error } = useDownloads();
  const cancelMutation = useCancelDownload();
  const clearCompleted = useClearCompleted();
  const scanLibrary = useScanLibrary();

  const { startPolling, stopPolling, setActiveCount } = useDownloadPollStore();

  const [cancellingId, setCancellingId] = useState<string | null>(null);

  // Track which succeeded downloads we have already triggered a scan for
  const scannedIds = useRef<Set<string>>(new Set());
  const scanTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  // ─── Polling control (mount / unmount) ────────────────────────────

  useEffect(() => {
    startPolling();
    return () => stopPolling();
  }, [startPolling, stopPolling]);

  // ─── Cleanup scan timer on unmount ────────────────────────────────

  useEffect(() => {
    return () => {
      if (scanTimer.current) clearTimeout(scanTimer.current);
    };
  }, []);

  // ─── Active count badge ───────────────────────────────────────────

  useEffect(() => {
    if (!downloads) return;
    const active = downloads.filter(
      (d) => !COMPLETED_STATES.has(d.state),
    ).length;
    setActiveCount(active);
  }, [downloads, setActiveCount]);

  // ─── Auto-scan on completion (debounced 30 s) ─────────────────────

  useEffect(() => {
    if (!downloads) return;

    const newlySucceeded = downloads.filter(
      (d) => d.state === "succeeded" && !scannedIds.current.has(d.id),
    );

    if (newlySucceeded.length > 0) {
      // New downloads completed — reset the debounce timer
      if (scanTimer.current) clearTimeout(scanTimer.current);
      scanTimer.current = setTimeout(() => {
        // Mark all currently-succeeded IDs as scanned so they are not re-processed.
        // Intentionally includes pre-existing succeeded downloads from before this
        // session — we only auto-scan once per download ID per page load.
        for (const d of downloads) {
          if (d.state === "succeeded") scannedIds.current.add(d.id);
        }
        scanLibrary.mutate(undefined, {
          onSuccess: (stats) => {
            toast.success(
              `Library scanned: ${stats.imported} tracks imported`,
            );
          },
          onError: (err) => {
            toast.error(
              `Scan failed: ${err instanceof Error ? err.message : "Unknown error"}`,
            );
          },
        });
      }, 30_000);
    }
    // NOTE: no cleanup returned — the timer must survive across poll ticks
  }, [downloads, scanLibrary]);

  // ─── Cancel single download ───────────────────────────────────────

  const handleCancel = useCallback(
    (id: string) => {
      setCancellingId(id);
      cancelMutation.mutate(id, {
        onSuccess: () => {
          toast.success("Download cancelled");
          setCancellingId(null);
        },
        onError: (err) => {
          toast.error(
            `Cancel failed: ${err instanceof Error ? err.message : "Unknown error"}`,
          );
          setCancellingId(null);
        },
      });
    },
    [cancelMutation],
  );

  // ─── Clear finished ───────────────────────────────────────────────

  const handleClearFinished = useCallback(() => {
    clearCompleted.mutate(undefined, {
      onSuccess: () => {
        toast.success("Finished downloads cleared");
        scannedIds.current.clear();
      },
      onError: (err) => {
        toast.error(
          `Clear failed: ${err instanceof Error ? err.message : "Unknown error"}`,
        );
      },
    });
  }, [clearCompleted]);

  // ─── Derived data ─────────────────────────────────────────────────

  const activeDownloads =
    downloads?.filter((d) => !COMPLETED_STATES.has(d.state)) ?? [];
  const finishedDownloads =
    downloads?.filter((d) => COMPLETED_STATES.has(d.state)) ?? [];
  const hasFinished = finishedDownloads.length > 0;

  // ─── Loading state ────────────────────────────────────────────────

  if (isLoading) {
    return (
      <div>
        <h1 className="mb-6 text-2xl font-bold text-white">Downloads</h1>
        <div className="flex items-center justify-center py-20">
          <Spinner size="lg" />
        </div>
      </div>
    );
  }

  // ─── Error state ──────────────────────────────────────────────────

  if (isError) {
    return (
      <div>
        <h1 className="mb-6 text-2xl font-bold text-white">Downloads</h1>
        <StatusMessage
          variant="error"
          message={error instanceof Error ? error.message : "Failed to load downloads"}
        />
      </div>
    );
  }

  // ─── Empty state ──────────────────────────────────────────────────

  if (!downloads || downloads.length === 0) {
    return (
      <div>
        <h1 className="mb-6 text-2xl font-bold text-white">Downloads</h1>
        <p className="py-20 text-center text-slate-500">No active downloads</p>
      </div>
    );
  }

  // ─── Main UI ──────────────────────────────────────────────────────

  return (
    <div>
      {/* Header */}
      <div className="mb-6 flex items-center justify-between">
        <h1 className="text-2xl font-bold text-white">Downloads</h1>
        {hasFinished && (
          <Button
            variant="ghost"
            size="sm"
            loading={clearCompleted.isPending}
            onClick={handleClearFinished}
          >
            Clear Finished
          </Button>
        )}
      </div>

      {/* Active downloads */}
      {activeDownloads.length > 0 && (
        <div className="mb-6">
          {activeDownloads.map((d) => (
            <DownloadItem
              key={d.id}
              download={d}
              onCancel={handleCancel}
              isCancelling={cancellingId === d.id}
            />
          ))}
        </div>
      )}

      {/* No-active message when only finished remain */}
      {activeDownloads.length === 0 && hasFinished && (
        <p className="py-16 text-center text-sm text-slate-500">
          No active downloads
        </p>
      )}

      {/* Finished downloads */}
      {hasFinished && (
        <div>
          <h2 className="mb-3 text-sm font-semibold uppercase tracking-wide text-slate-400">
            Finished
          </h2>
          {finishedDownloads.map((d) => (
            <DownloadItem
              key={d.id}
              download={d}
              onCancel={handleCancel}
              isCancelling={false}
            />
          ))}
        </div>
      )}
    </div>
  );
}

export default DownloadsPage;
