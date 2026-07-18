import type { FC } from "react";
import {
  Search,
  Download,
  Music,
  ListMusic,
  Settings,
} from "lucide-react";

export interface NavPage {
  id: string;
  label: string;
  icon: "search" | "downloads" | "library" | "playlists" | "settings";
  href: string;
}

interface SidebarNavProps {
  activePage: string;
  onNavigate: (href: string) => void;
  downloadCount?: number;
  className?: string;
}

const DEFAULT_PAGES: NavPage[] = [
  { id: "search", label: "Search", icon: "search", href: "/search" },
  { id: "downloads", label: "Downloads", icon: "downloads", href: "/downloads" },
  { id: "library", label: "Library", icon: "library", href: "/library" },
  { id: "playlists", label: "Playlists", icon: "playlists", href: "/playlists" },
];

const iconMap: Record<string, typeof Search> = {
  search: Search,
  downloads: Download,
  library: Music,
  playlists: ListMusic,
  settings: Settings,
};

const SidebarNav: FC<SidebarNavProps> = ({
  activePage,
  onNavigate,
  downloadCount,
  className = "",
}) => {
  return (
    <nav
      className={`flex h-full flex-col bg-slate-900 border-r border-slate-800 ${className}`}
      aria-label="Main navigation"
    >
      {/* Brand */}
      <div className="flex items-center gap-2 px-4 py-5">
        <span className="text-xl font-bold text-purple-400">♫</span>
        <span className="text-lg font-semibold text-white">Groovearr</span>
      </div>

      {/* Nav links */}
      <div className="flex flex-1 flex-col gap-0.5 px-2">
        {DEFAULT_PAGES.map((page) => {
          const Icon = iconMap[page.icon];
          const isActive = activePage === page.id;
          return (
            <button
              key={page.id}
              type="button"
              onClick={() => onNavigate(page.href)}
              className={`flex items-center gap-3 rounded-lg px-3 py-2.5 text-sm font-medium transition-colors ${
                isActive
                  ? "border-l-4 border-purple-500 bg-slate-800 text-white"
                  : "border-l-4 border-transparent text-slate-400 hover:bg-slate-800 hover:text-slate-200"
              }`}
            >
              <Icon size={18} />
              <span>{page.label}</span>
              {page.id === "downloads" && downloadCount != null && downloadCount > 0 && (
                <span className="ml-auto flex h-5 min-w-[20px] items-center justify-center rounded-full bg-purple-600 px-1.5 text-[10px] font-bold text-white">
                  {downloadCount}
                </span>
              )}
            </button>
          );
        })}
      </div>

      {/* Settings footer */}
      <div className="border-t border-slate-800 px-2 py-3">
        <button
          type="button"
          onClick={() => onNavigate("/settings")}
          className={`flex w-full items-center gap-3 rounded-lg px-3 py-2.5 text-sm font-medium transition-colors ${
            activePage === "settings"
              ? "border-l-4 border-purple-500 bg-slate-800 text-white"
              : "border-l-4 border-transparent text-slate-400 hover:bg-slate-800 hover:text-slate-200"
          }`}
        >
          <Settings size={18} />
          <span>Settings</span>
        </button>
      </div>
    </nav>
  );
};

export default SidebarNav;
