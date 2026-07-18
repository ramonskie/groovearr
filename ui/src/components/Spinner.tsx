import type { FC } from "react";

type SpinnerSize = "sm" | "md" | "lg";

interface SpinnerProps {
  size?: SpinnerSize;
  className?: string;
}

const sizeMap: Record<SpinnerSize, string> = {
  sm: "h-4 w-4 border-2",
  md: "h-6 w-6 border-2",
  lg: "h-10 w-10 border-[3px]",
};

const Spinner: FC<SpinnerProps> = ({ size = "md", className = "" }) => {
  return (
    <div
      role="status"
      aria-label="Loading"
      className={`inline-block rounded-full border-slate-700 border-t-purple-500 animate-spin ${sizeMap[size]} ${className}`}
    />
  );
};

export default Spinner;
