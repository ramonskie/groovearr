import { useState, useCallback } from "react";
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
import { useScanOnComplete } from "../../hooks/use-scan-on-complete";
import { toast } from "sonner";
import Button from "../../components/Button";
import Spinner from "../../components/Spinner";
import StatusMessage from "../../components/StatusMessage";
import DownloadItem from "./DownloadItem";
import PendingDownloadList from "./PendingDownloadList";

// ─── Constants ──────────────────────────────────────────────────────

type Tab = "pending" | "finished" | "failed";

const TERMINAL_STATES = new Set<DownloadState>(["imported", "ignored"]);
const FAILED_STATES = new Set<DownloadState>(["failed"]);

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

  useDownloadEvents();
  const { resetScannedIds } = useScanOnComplete({ downloads, scanLibrary });

  const [cancellingId, setCancellingId] = useState<string | null>(null);
  const [searchParams, setSearchParams] = useSearchParams();
  const activeTab: Tab = (() => {
    const fromParam = searchParams.get("downloadTab");
    if (fromParam === "pending" || fromParam === "finished" || fromParam === "failed") return fromParam;
    return "pending";
  })();

  const handleCancel = useCallback(
    (id: string) => {
      setCancellingId(id);
      cancelMutation.mutate(id, {
        onSuccess: () => {
          toast.success("Download cancelled");
          setCancellingId(null);
        },
        onError: (err) => {
          toast.error(`Cancel failed: ${err instanceof Error ? err.message : "Unknown error"}`);
          setCancellingId(null);
        },
      });
    },
    [cancelMutation],
  );

  const handleRetry = useCallback(
    (id: string) => {
      setCancellingId(id);
      retryMutation.mutate(id, {
        onSuccess: () => {
          toast.success("Download queued for retry");
          setCancellingId(null);
        },
        onError: (err) => {
          toast.error(`Retry failed: ${err instanceof Error ? err.message : "Unknown error"}`);
          setCancellingId(null);
        },
      });
    },
    [retryMutation],
  );

  const handleClearFinished = useCallback(() => {
    clearCompleted.mutate(undefined, {
      onSuccess: () => {
        toast.success("Finished downloads cleared");
        resetScannedIds();
      },
      onError: (err) => {
        toast.error(`Clear failed: ${err instanceof Error ? err.message : "Unknown error"}`);
      },
    });
  }, [clearCompleted, resetScannedIds]);

  // ─── Derived data ─────────────────────────────────────────────────

  const pendingDownloads =
    downloads?.filter((d) => !TERMINAL_STATES.has(d.state) && !FAILED_STATES.has(d.state)) ?? [];
  const finishedDownloads =
    downloads?.filter((d) => TERMINAL_STATES.has(d.state)) ?? [];
  const failedDownloads =
    downloads?.filter((d) => FAILED_STATES.has(d.state)) ?? [];
  const hasFinished = finishedDownloads.length > 0;
  const hasFailed = failedDownloads.length > 0;

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
  const failedCount = failedDownloads.length || undefined;

  return (
    <div>
      {/* Header */}
      <div className="mb-6 flex items-center justify-between">
        <div className="flex items-center gap-3">
          <h1 className="text-2xl font-bold text-white">Downloads</h1>
          <span
            className={`inline-flex items-center gap-1.5 rounded px-2 py-0.5 text-xs ${
              sseActive ? "bg-green-900/50 text-green-400" : "bg-yellow-900/50 text-yellow-400"
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
        {hasFinished && activeTab !== "failed" && (
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
          ["failed", "Failed", failedCount],
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

      {/* Pending tab */}
      {activeTab === "pending" && (
        <PendingDownloadList
          pendingDownloads={pendingDownloads}
          handleCancel={handleCancel}
          handleRetry={handleRetry}
          cancellingId={cancellingId}
        />
      )}

      {/* Finished tab */}
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
          <p className="py-16 text-center text-sm text-slate-500">No finished downloads</p>
        ))}

      {/* Failed tab */}
      {activeTab === "failed" &&
        (hasFailed ? (
          <div>
            {failedDownloads.map((d) => (
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
          <p className="py-16 text-center text-sm text-slate-500">No failed downloads</p>
        ))}
    </div>
  );
}

export default DownloadsPage;
