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
import { useAuth } from "./context/AuthContext";
import { useDownloads } from "./hooks/use-downloads";
import type { DownloadState } from "./api/types";

const LoginPage = lazy(() => import("./features/auth/LoginPage"));

export type PageName =
  | "discover"
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
const DiscoverPage = lazy(
  () => import("./features/discover/DiscoverPage"),
);

// ─── Suspense fallback ───────────────────────────────────────────────

function PageFallback() {
  return (
    <div className="flex items-center justify-center py-20">
      <Spinner size="lg" />
    </div>
  );
}

function FullPageFallback() {
  return (
    <div className="flex min-h-screen items-center justify-center bg-zinc-950">
      <Spinner size="lg" />
    </div>
  );
}

// ─── Helpers ─────────────────────────────────────────────────────────

const VALID_PAGES = new Set<string>([
  "discover",
  "search",
  "downloads",
  "library",
  "playlists",
  "settings",
]);

const TERMINAL_STATES = new Set<DownloadState>([
  "imported",
  "failed",
  "ignored",
]);

function pathToPage(pathname: string): PageName {
  const page = pathname.replace("/", "") || "discover";
  return VALID_PAGES.has(page) ? (page as PageName) : "search";
}

// ─── App shell ───────────────────────────────────────────────────────

function AppShell() {
  const location = useLocation();
  const navigate = useNavigate();
  const { data: downloads } = useDownloads();
  const { logout } = useAuth();

  const activePage = pathToPage(location.pathname);

  const activeDownloadCount =
    downloads?.filter((d) => !TERMINAL_STATES.has(d.state)).length ?? 0;

  const sidebar = (
    <SidebarNav
      activePage={activePage}
      onNavigate={(href) => navigate(href)}
      downloadCount={activeDownloadCount}
      onLogout={() => logout()}
    />
  );

  return (
    <Layout sidebar={sidebar}>
      <Suspense fallback={<PageFallback />}>
        <Routes>
          <Route index element={<Navigate to="/discover" replace />} />
          <Route path="/discover" element={<DiscoverPage />} />
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
  const { isAuthenticated, isLoading } = useAuth();

  if (isLoading) {
    return <FullPageFallback />;
  }

  return (
    <Routes>
      <Route
        path="/login"
        element={
          isAuthenticated ? (
            <Navigate to="/discover" replace />
          ) : (
            <Suspense fallback={<FullPageFallback />}>
              <LoginPage />
            </Suspense>
          )
        }
      />
      <Route
        path="*"
        element={
          isAuthenticated ? (
            <AppShell />
          ) : (
            <Navigate to="/login" replace />
          )
        }
      />
    </Routes>
  );
}
