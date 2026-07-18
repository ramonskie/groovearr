import { lazy, Suspense } from "react";
import {
  Routes,
  Route,
  Navigate,
  useLocation,
  useNavigate,
} from "react-router-dom";
import Layout from "./components/Layout";
import SidebarNav from "./components/SidebarNav";
import Spinner from "./components/Spinner";
import { useDownloads } from "./hooks/use-downloads";
import type { DownloadState } from "./api/types";

export type PageName =
  | "search"
  | "downloads"
  | "library"
  | "playlists"
  | "settings";

// ─── Lazy-loaded pages ───────────────────────────────────────────────

const SearchPage = lazy(() => import("./features/search/SearchPage"));
const DownloadsPage = lazy(
  () => import("./features/downloads/DownloadsPage"),
);
const LibraryPage = lazy(() => import("./features/library/LibraryPage"));
const PlaylistsPage = lazy(
  () => import("./features/playlists/PlaylistsPage"),
);
const SettingsPage = lazy(
  () => import("./features/settings/SettingsPage"),
);

// ─── Suspense fallback ───────────────────────────────────────────────

function PageFallback() {
  return (
    <div className="flex items-center justify-center py-20">
      <Spinner size="lg" />
    </div>
  );
}

// ─── Helpers ─────────────────────────────────────────────────────────

const VALID_PAGES = new Set<string>([
  "search",
  "downloads",
  "library",
  "playlists",
  "settings",
]);

const TERMINAL_STATES = new Set<DownloadState>([
  "succeeded",
  "errored",
  "cancelled",
  "aborted",
]);

function pathToPage(pathname: string): PageName {
  const page = pathname.replace("/", "") || "search";
  return VALID_PAGES.has(page) ? (page as PageName) : "search";
}

// ─── App shell ───────────────────────────────────────────────────────

function AppShell() {
  const location = useLocation();
  const navigate = useNavigate();
  const { data: downloads } = useDownloads();

  const activePage = pathToPage(location.pathname);

  // Count downloads that are still in progress (non-terminal states).
  // NOTE: This triggers a downloads fetch on every page, not just /downloads.
  // TanStack Query deduplicates by key so no double-fetch when already on
  // DownloadsPage.  The tradeoff is acceptable for the sidebar badge.
  const activeDownloadCount =
    downloads?.filter((d) => !TERMINAL_STATES.has(d.state)).length ?? 0;

  const sidebar = (
    <SidebarNav
      activePage={activePage}
      onNavigate={(href) => navigate(href)}
      downloadCount={activeDownloadCount}
    />
  );

  return (
    <Layout sidebar={sidebar}>
      <Suspense fallback={<PageFallback />}>
        <Routes>
          <Route index element={<Navigate to="/search" replace />} />
          <Route path="/search" element={<SearchPage />} />
          <Route path="/downloads" element={<DownloadsPage />} />
          <Route path="/library" element={<LibraryPage />} />
          <Route path="/playlists" element={<PlaylistsPage />} />
          <Route path="/settings" element={<SettingsPage />} />
        </Routes>
      </Suspense>
    </Layout>
  );
}

// ─── App root ────────────────────────────────────────────────────────

export default function App() {
  return <AppShell />;
}
