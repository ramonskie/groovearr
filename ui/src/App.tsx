import { lazy, Suspense, type ReactNode } from "react";
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
import { useSetupStatus } from "./hooks/use-config";
import { useJobWatcher } from "./hooks/use-job";
import { useTrackingEvents } from "./hooks/use-tracking-events";
import type { DownloadState } from "./api/types";

const LoginPage = lazy(() => import("./features/auth/LoginPage"));
const SetupWizard = lazy(() => import("./features/setup/SetupWizard"));

export type PageName =
  | "discover"
  | "search"
  | "downloads"
  | "library"
  | "playlists"
  | "tracking"
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
const TrackingPage = lazy(
  () => import("./features/tracking/TrackingPage"),
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
  "tracking",
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

// Redirects to the first-run wizard until setup is dismissed, and away from it
// once the flag lands. Both "Done" and "Skip for now" persist setup_completed;
// the refetch after that PUT is async, so the gate must be bidirectional to
// clear the wizard once needs_setup flips to false.
function SetupGate() {
  const location = useLocation();
  const { data } = useSetupStatus();

  if (data?.needs_setup && location.pathname !== "/setup") {
    return <Navigate to="/setup" replace />;
  }
  if (data && !data.needs_setup && location.pathname === "/setup") {
    return <Navigate to="/discover" replace />;
  }
  return null;
}

// ─── Route guards ────────────────────────────────────────────────────

/**
 * Blocks the admin-only surface for everyone else. `isAdmin` is only true when
 * the resolved role is exactly "admin"; an unresolved role (null) is therefore
 * treated as non-admin, so an unknown role can never briefly expose settings or
 * tracking. Redirects to /discover — a page every authenticated user may use.
 */
function RequireAdmin({
  isAdmin,
  children,
}: {
  isAdmin: boolean;
  children: ReactNode;
}) {
  if (!isAdmin) {
    return <Navigate to="/discover" replace />;
  }
  return <>{children}</>;
}

function AppShell() {
  const location = useLocation();
  const navigate = useNavigate();
  const { data: downloads } = useDownloads();
  const { logout, authMethod, isAdmin } = useAuth();

  // Keep the background-job query alive app-wide and refresh the library when
  // a scan/enrich job finishes. GET /api/jobs is admin-only: the hook must be
  // called unconditionally (Rules of Hooks), so the gate is passed in as an
  // `enabled` flag — a non-admin never issues the request.
  useJobWatcher(isAdmin);

  // Refresh tracking views (wanted badge, album statuses) on import completion
  // app-wide — imports can be started from any page, not just Downloads.
  useTrackingEvents();

  const activePage = pathToPage(location.pathname);

  const activeDownloadCount =
    downloads?.filter((d) => !TERMINAL_STATES.has(d.state)).length ?? 0;

  const isAuthEnabled = authMethod !== "" && authMethod !== "none";

  const sidebar = (
    <SidebarNav
      activePage={activePage}
      onNavigate={(href) => navigate(href)}
      downloadCount={activeDownloadCount}
      onLogout={isAuthEnabled ? () => logout() : undefined}
      isAdmin={isAdmin}
    />
  );

  return (
    <Layout sidebar={sidebar}>
      <SetupGate />
      <Suspense fallback={<PageFallback />}>
        <Routes>
          <Route index element={<Navigate to="/discover" replace />} />
          <Route path="/discover" element={<DiscoverPage />} />
          <Route path="/search" element={<SearchPage />} />
          <Route path="/downloads" element={<DownloadsPage />} />
          <Route path="/library" element={<LibraryPage />} />
          <Route path="/playlists" element={<PlaylistsPage />} />
          <Route
            path="/tracking"
            element={
              <RequireAdmin isAdmin={isAdmin}>
                <TrackingPage />
              </RequireAdmin>
            }
          />
          <Route
            path="/settings/*"
            element={
              <RequireAdmin isAdmin={isAdmin}>
                <SettingsPage />
              </RequireAdmin>
            }
          />
          <Route path="/setup" element={<SetupWizard />} />
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
