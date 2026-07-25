import { useEffect, useRef, useState, useCallback } from "react";
import { useSearchParams } from "react-router-dom";
import type { DownloadState } from "../../api/types";
import {
  useDownloads,
  useCancelDownload,
  useRetryDownload,
  useClearCompleted,
} from "../../hooks/use-downloads";
import { useScanLibrary } from "../../hooks/use-library";
import { useDownloadEvents } from "../../hooks/use-download-events";
import { toast } from "sonner";
import Button from "../../components/Button";
import Spinner from "../../components/Spinner";
import StatusMessage from "../../components/StatusMessage";
import DownloadItem from "./DownloadItem";

// ─── Constants ──────────────────────────────────────────────────────

type Tab = "pending" | "finished";

const TERMINAL_STATES = new Set<DownloadState>([
  "imported",
  "failed",
  "ignored",
]);

// ─── Component ──────────────────────────────────────────────────────

function DownloadsPage() {
  const {
    data: downloads,
    isLoading,
    isError,
    error,
    sseActive,
    sseStatus,
  } = useDownloads();
  const cancelMutation = useCancelDownload();
  const retryMutation = useRetryDownload();
  const clearCompleted = useClearCompleted();
  const scanLibrary = useScanLibrary();

  // Activate SSE connection.
  useDownloadEvents();

  const [cancellingId, setCancellingId] = useState<string | null>(null);
  const [searchParams, setSearchParams] = useSearchParams();
  const activeTab: Tab = (() => {
    const fromParam = searchParams.get("downloadTab");
    if (fromParam === "pending" || fromParam === "finished") return fromParam;
    return "pending";
  })();
  const [showAllQueued, setShowAllQueued] = useState(false);

  // Track which succeeded downloads we have already triggered a scan for.
  const scannedIds = useRef<Set<string>>(new Set());
  const scanTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  // ─── Cleanup scan timer on unmount ────────────────────────────────

  useEffect(() => {
    return () => {
      if (scanTimer.current) clearTimeout(scanTimer.current);
    };
  }, []);

  // ─── Auto-scan on completion (debounced 30 s) ─────────────────────

  useEffect(() => {
    if (!downloads || downloads.length === 0) return;

    const newlySucceeded = downloads.filter(
      (d) => d.state === "imported" && !scannedIds.current.has(d.id),
    );

    if (newlySucceeded.length > 0) {
      if (scanTimer.current) clearTimeout(scanTimer.current);
      scanTimer.current = setTimeout(() => {
        for (const d of downloads) {
          if (d.state === "imported") scannedIds.current.add(d.id);
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

  // ─── Retry single download ─────────────────────────────────────────

  const handleRetry = useCallback(
    (id: string) => {
      setCancellingId(id);
      retryMutation.mutate(id, {
        onSuccess: () => {
          toast.success("Download queued for retry");
          setCancellingId(null);
        },
        onError: (err) => {
          toast.error(
            `Retry failed: ${err instanceof Error ? err.message : "Unknown error"}`,
          );
          setCancellingId(null);
        },
      });
    },
    [retryMutation],
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

  const pendingDownloads =
    downloads?.filter((d) => !TERMINAL_STATES.has(d.state)) ?? [];
  const finishedDownloads =
    downloads?.filter((d) => TERMINAL_STATES.has(d.state)) ?? [];
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

  // ─── Main UI ──────────────────────────────────────────────────────

  const pendingCount = pendingDownloads.length || undefined;
  const finishedCount = finishedDownloads.length || undefined;

  return (
    <div>
      {/* Header */}
      <div className="mb-6 flex items-center justify-between">
        <div className="flex items-center gap-3">
          <h1 className="text-2xl font-bold text-white">Downloads</h1>
          <span
            className={`inline-flex items-center gap-1.5 rounded px-2 py-0.5 text-xs ${
              sseActive
                ? "bg-green-900/50 text-green-400"
                : "bg-yellow-900/50 text-yellow-400"
            }`}
            title={`SSE: ${sseStatus}`}
          >
            <span
              className={`inline-block h-1.5 w-1.5 rounded-full ${
                sseStatus === "connected" ? "bg-green-400" : "bg-yellow-400"
              }`}
            />
            {sseStatus === "connected" ? "Live" : "Polling"}
          </span>
        </div>
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

      {/* Tabs */}
      <div className="mb-4 flex gap-1 border-b border-slate-700/50">
        {([
          ["pending", "Pending", pendingCount],
          ["finished", "Finished", finishedCount],
        ] as const).map(([tab, label, count]) => (
          <button
            key={tab}
            type="button"
            className={`flex items-center gap-1.5 px-4 py-2 text-sm font-medium transition-colors ${
              activeTab === tab
                ? "border-b-2 border-blue-500 text-white"
                : "text-slate-400 hover:text-slate-300"
            }`}
            onClick={() => {
              setSearchParams((prev) => {
                const next = new URLSearchParams(prev);
                next.set("downloadTab", tab);
                return next;
              });
            }}
          >
            {label}
            {count !== undefined && (
              <span
                className={`rounded-full px-1.5 text-xs ${
                  activeTab === tab
                    ? "bg-blue-500/20 text-blue-300"
                    : "bg-slate-800 text-slate-400"
                }`}
              >
                {count}
              </span>
            )}
          </button>
        ))}
      </div>

      {/* Pending tab: queue first, then active */}
      {activeTab === "pending" && (() => {
        const queued = pendingDownloads.filter(d => d.state === "queued");
        const failedPending = pendingDownloads.filter(d => d.state === "failedPending");
        const active = pendingDownloads.filter(d => d.state !== "queued" && d.state !== "failedPending");

        if (queued.length === 0 && active.length === 0 && failedPending.length === 0) {
          return <p className="py-16 text-center text-sm text-slate-500">No pending downloads</p>;
        }

        return (
          <div className="mb-6">
            {active.length > 0 && (
              <div className="mb-4">
                <h2 className="mb-2 text-xs font-semibold uppercase tracking-wide text-slate-500">
                  Active &mdash; {active.length} downloading
                </h2>
                {active.map((d) => (
                  <DownloadItem
                    key={d.id}
                    download={d}
                    onCancel={handleCancel}
                    onRetry={handleRetry}
                    isCancelling={cancellingId === d.id}
                  />
                ))}
              </div>
            )}
            {failedPending.length > 0 && (
              <div className="mb-4">
                <h2 className="mb-2 text-xs font-semibold uppercase tracking-wide text-slate-500">
                  Retrying &mdash; {failedPending.length} waiting
                </h2>
                {failedPending.map((d) => (
                  <DownloadItem
                    key={d.id}
                    download={d}
                    onCancel={handleCancel}
                    onRetry={handleRetry}
                    isCancelling={cancellingId === d.id}
                  />
                ))}
              </div>
            )}
            {(active.length > 0 || failedPending.length > 0) && queued.length > 0 && (
              <hr className="my-4 border-slate-700/50" />
            )}
            {queued.length > 0 && (() => {
              const QUEUE_PREVIEW = 20;
              const visible = showAllQueued ? queued : queued.slice(0, QUEUE_PREVIEW);
              const collapsed = queued.length > QUEUE_PREVIEW && !showAllQueued;

              return (
                <div>
                  <h2 className="mb-2 text-xs font-semibold uppercase tracking-wide text-slate-500">
                    Queue &mdash; {queued.length} waiting
                  </h2>
                  {visible.map((d) => (
                    <DownloadItem
                      key={d.id}
                      download={d}
                      onCancel={handleCancel}
                      onRetry={handleRetry}
                      isCancelling={cancellingId === d.id}
                    />
                  ))}
                  {collapsed && (
                    <button
                      type="button"
                      onClick={() => setShowAllQueued(true)}
                      className="mt-2 w-full rounded-lg border border-slate-700 bg-slate-800/50 py-2 text-xs text-slate-400 transition-colors hover:bg-slate-800 hover:text-slate-300"
                    >
                      Show all {queued.length} queued items
                    </button>
                  )}
                  {showAllQueued && queued.length > QUEUE_PREVIEW && (
                    <button
                      type="button"
                      onClick={() => setShowAllQueued(false)}
                      className="mt-2 w-full rounded-lg border border-slate-700 bg-slate-800/50 py-2 text-xs text-slate-400 transition-colors hover:bg-slate-800 hover:text-slate-300"
                    >
                      Show less
                    </button>
                  )}
                </div>
              );
            })()}
          </div>
        );
      })()}

      {/* Finished tab: terminal downloads */}
      {activeTab === "finished" &&
        (hasFinished ? (
          <div>
            {finishedDownloads.map((d) => (
              <DownloadItem
                key={d.id}
                download={d}
                onCancel={handleCancel}
                onRetry={handleRetry}
                isCancelling={cancellingId === d.id}
              />
            ))}
          </div>
        ) : (
          <p className="py-16 text-center text-sm text-slate-500">
            No finished downloads
          </p>
        ))}
    </div>
  );
}

export default DownloadsPage;
