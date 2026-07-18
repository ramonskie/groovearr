import type { FC, ReactNode } from "react";
import { X } from "lucide-react";

type StatusVariant = "info" | "success" | "error";

interface StatusMessageProps {
  variant?: StatusVariant;
  message?: string;
  onDismiss?: () => void;
  className?: string;
  children?: ReactNode;
}

const variantClasses: Record<StatusVariant, string> = {
  info: "border-blue-800 bg-blue-950/50 text-blue-300",
  success: "border-green-800 bg-green-950/50 text-green-300",
  error: "border-red-800 bg-red-950/50 text-red-300",
};

const iconColors: Record<StatusVariant, string> = {
  info: "text-blue-400",
  success: "text-green-400",
  error: "text-red-400",
};

const StatusMessage: FC<StatusMessageProps> = ({
  variant = "info",
  message,
  onDismiss,
  className = "",
  children,
}) => {
  return (
    <div
      role="alert"
      className={`flex items-start gap-3 rounded-lg border p-4 ${variantClasses[variant]} ${className}`}
    >
      <div className="flex-1 text-sm">
        {message}
        {children}
      </div>
      {onDismiss && (
        <button
          type="button"
          onClick={onDismiss}
          aria-label="Dismiss"
          className={`shrink-0 rounded p-0.5 hover:bg-slate-800/50 ${iconColors[variant]}`}
        >
          <X size={16} />
        </button>
      )}
    </div>
  );
};

export default StatusMessage;
