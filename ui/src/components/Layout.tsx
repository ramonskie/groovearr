import type { FC, ReactNode } from "react";

interface LayoutProps {
  sidebar: ReactNode;
  children: ReactNode;
  className?: string;
}

const Layout: FC<LayoutProps> = ({ sidebar, children, className = "" }) => {
  return (
    <div className={`flex h-screen overflow-hidden bg-slate-950 ${className}`}>
      <aside className="w-56 shrink-0">{sidebar}</aside>
      <main className="flex-1 overflow-y-auto p-6">{children}</main>
    </div>
  );
};

export default Layout;
