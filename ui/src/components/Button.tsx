import type { ButtonHTMLAttributes, FC, ReactNode } from "react";
import Spinner from "./Spinner";

type ButtonVariant = "primary" | "success" | "danger" | "ghost";
type ButtonSize = "sm" | "default";

interface ButtonBaseProps {
  variant?: ButtonVariant;
  size?: ButtonSize;
  loading?: boolean;
  children: ReactNode;
  className?: string;
}

type ButtonProps = ButtonBaseProps &
  Omit<ButtonHTMLAttributes<HTMLButtonElement>, keyof ButtonBaseProps>;

const variantClasses: Record<ButtonVariant, string> = {
  primary:
    "bg-purple-600 text-white hover:bg-purple-700 focus-visible:ring-purple-500",
  success:
    "bg-green-700 text-white hover:bg-green-800 focus-visible:ring-green-500",
  danger: "bg-red-700 text-white hover:bg-red-800 focus-visible:ring-red-500",
  ghost:
    "border border-slate-700 bg-transparent text-slate-300 hover:bg-slate-800 hover:text-white focus-visible:ring-slate-500",
};

const sizeClasses: Record<ButtonSize, string> = {
  sm: "px-3 py-1.5 text-xs",
  default: "px-4 py-2 text-sm",
};

const Button: FC<ButtonProps> = ({
  variant = "primary",
  size = "default",
  loading = false,
  children,
  className = "",
  disabled,
  ...rest
}) => {
  return (
    <button
      disabled={disabled || loading}
      className={`inline-flex items-center justify-center gap-2 rounded-lg font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-offset-2 focus-visible:ring-offset-slate-950 disabled:cursor-not-allowed disabled:opacity-50 ${variantClasses[variant]} ${sizeClasses[size]} ${className}`}
      {...rest}
    >
      {loading && <Spinner size="sm" />}
      {children}
    </button>
  );
};

export default Button;
