import type { FC, ReactNode } from "react";

interface CardProps {
  title?: string;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}

const Card: FC<CardProps> = ({ title, actions, children, className = "" }) => {
  return (
    <div className={`mb-4 rounded-lg border border-slate-800 bg-slate-900 ${className}`}>
      {(title || actions) && (
        <div className="flex items-center justify-between border-b border-slate-800 px-4 py-3">
          {title && (
            <h3 className="text-sm font-semibold text-white">{title}</h3>
          )}
          {actions && <div className="flex items-center gap-2">{actions}</div>}
        </div>
      )}
      <div className="p-4">{children}</div>
    </div>
  );
};

export default Card;
