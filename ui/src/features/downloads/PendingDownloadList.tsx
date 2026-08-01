import { useState } from "react";
import type { DownloadRecord as DownloadItemType } from "../../api/types";
import DownloadItem from "./DownloadItem";

interface PendingDownloadListProps {
  pendingDownloads: DownloadItemType[];
  handleCancel: (id: string) => void;
  handleRetry: (id: string) => void;
  cancellingId: string | null;
}

const QUEUE_PREVIEW = 20;

export default function PendingDownloadList({
  pendingDownloads,
  handleCancel,
  handleRetry,
  cancellingId,
}: PendingDownloadListProps) {
  const [showAllQueued, setShowAllQueued] = useState(false);

  const queued = pendingDownloads.filter((d) => d.state === "queued");
  const failedPending = pendingDownloads.filter((d) => d.state === "failedPending");
  const active = pendingDownloads.filter((d) => d.state !== "queued" && d.state !== "failedPending");

  if (queued.length === 0 && active.length === 0 && failedPending.length === 0) {
    return <p className="py-16 text-center text-sm text-slate-500">No pending downloads</p>;
  }

  const itemProps = { onCancel: handleCancel, onRetry: handleRetry };

  return (
    <div className="mb-6">
      {active.length > 0 && (
        <div className="mb-4">
          <h2 className="mb-2 text-xs font-semibold uppercase tracking-wide text-slate-500">
            Active &mdash; {active.length} downloading
          </h2>
          {active.map((d) => (
            <DownloadItem key={d.id} download={d} {...itemProps} isCancelling={cancellingId === d.id} />
          ))}
        </div>
      )}
      {failedPending.length > 0 && (
        <div className="mb-4">
          <h2 className="mb-2 text-xs font-semibold uppercase tracking-wide text-slate-500">
            Retrying &mdash; {failedPending.length} waiting
          </h2>
          {failedPending.map((d) => (
            <DownloadItem key={d.id} download={d} {...itemProps} isCancelling={cancellingId === d.id} />
          ))}
        </div>
      )}
      {(active.length > 0 || failedPending.length > 0) && queued.length > 0 && (
        <hr className="my-4 border-slate-700/50" />
      )}
      {queued.length > 0 && (
        <QueueSection
          queued={queued}
          showAllQueued={showAllQueued}
          setShowAllQueued={setShowAllQueued}
          itemProps={itemProps}
          cancellingId={cancellingId}
        />
      )}
    </div>
  );
}

function QueueSection({
  queued,
  showAllQueued,
  setShowAllQueued,
  itemProps,
  cancellingId,
}: {
  queued: DownloadItemType[];
  showAllQueued: boolean;
  setShowAllQueued: (v: boolean) => void;
  itemProps: { onCancel: (id: string) => void; onRetry: (id: string) => void };
  cancellingId: string | null;
}) {
  const visible = showAllQueued ? queued : queued.slice(0, QUEUE_PREVIEW);
  const collapsed = queued.length > QUEUE_PREVIEW && !showAllQueued;

  return (
    <div>
      <h2 className="mb-2 text-xs font-semibold uppercase tracking-wide text-slate-500">
        Queue &mdash; {queued.length} waiting
      </h2>
      {visible.map((d) => (
        <DownloadItem key={d.id} download={d} {...itemProps} isCancelling={cancellingId === d.id} />
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
}
