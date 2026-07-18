import type { FC, ReactNode } from "react";

interface FormGroupProps {
  label?: string;
  htmlFor?: string;
  hint?: ReactNode;
  error?: string;
  children: ReactNode;
  className?: string;
}

const FormGroup: FC<FormGroupProps> = ({
  label,
  htmlFor,
  hint,
  error,
  children,
  className = "",
}) => {
  return (
    <div className={`flex flex-col gap-1 ${className}`}>
      {label && (
        <label
          htmlFor={htmlFor}
          className="text-xs font-medium uppercase tracking-wider text-slate-400"
        >
          {label}
        </label>
      )}
      {children}
      {hint && !error && (
        <p className="mt-1 text-xs text-slate-500">{hint}</p>
      )}
      {error && (
        <p className="mt-1 text-xs text-red-400" role="alert">
          {error}
        </p>
      )}
    </div>
  );
};

export default FormGroup;
