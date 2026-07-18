import type { FC, ReactNode } from "react";

type BadgeVariant = "success" | "warning" | "error" | "muted";

interface BadgeProps {
  variant?: BadgeVariant;
  children: ReactNode;
  className?: string;
}

const variantClasses: Record<BadgeVariant, string> = {
  success: "bg-green-900/50 text-green-400 border-green-800",
  warning: "bg-yellow-900/50 text-yellow-400 border-yellow-800",
  error: "bg-red-900/50 text-red-400 border-red-800",
  muted: "bg-slate-800 text-slate-400 border-slate-700",
};

const Badge: FC<BadgeProps> = ({ variant = "muted", children, className = "" }) => {
  return (
    <span
      className={`inline-flex items-center rounded border px-2 py-0.5 text-xs font-semibold ${variantClasses[variant]} ${className}`}
    >
      {children}
    </span>
  );
};

export default Badge;
