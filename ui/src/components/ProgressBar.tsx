import type { FC } from "react";

interface ProgressBarProps {
  percentage: number;
  className?: string;
}

const ProgressBar: FC<ProgressBarProps> = ({ percentage, className = "" }) => {
  const clamped = Math.max(0, Math.min(100, percentage));

  return (
    <div
      role="progressbar"
      aria-valuenow={clamped}
      aria-valuemin={0}
      aria-valuemax={100}
      className={`h-1.5 w-full overflow-hidden rounded-full bg-slate-700 ${className}`}
    >
      <div
        className="h-full rounded-full bg-purple-500 transition-[width] duration-300 ease-in-out"
        style={{ width: `${clamped}%` }}
      />
    </div>
  );
};

export default ProgressBar;
