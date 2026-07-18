import type { FC } from "react";

interface DownloadProgressBarProps {
  percentage: number;
}

const DownloadProgressBar: FC<DownloadProgressBarProps> = ({ percentage }) => {
  const clamped = Math.max(0, Math.min(100, percentage));

  return (
    <div className="flex items-center gap-2">
      <div
        role="progressbar"
        aria-valuenow={clamped}
        aria-valuemin={0}
        aria-valuemax={100}
        className="h-1 flex-1 overflow-hidden rounded-full bg-slate-700"
      >
        <div
          className="h-full rounded-full bg-purple-500 transition-[width] duration-300 ease-in-out"
          style={{ width: `${clamped}%` }}
        />
      </div>
      <span className="min-w-[3ch] text-right text-xs tabular-nums text-slate-400">
        {Math.round(clamped)}%
      </span>
    </div>
  );
};

export default DownloadProgressBar;
