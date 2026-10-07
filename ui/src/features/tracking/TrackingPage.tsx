import { useEffect, useRef, useState } from "react";
import type { ChangeEvent } from "react";
import { Link } from "react-router-dom";
import {
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import type { QueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Radio, RefreshCw, Search } from "lucide-react";
import Spinner from "../../components/Spinner";
import StatusMessage from "../../components/StatusMessage";
import Button from "../../components/Button";
import Badge from "../../components/Badge";
import TrackingArtistDetailView from "./TrackingArtistDetailView";
import { useJobState } from "../../hooks/use-job";
import { useAuth } from "../../context/AuthContext";
import {
  listTrackedArtists,
  listAllWanted,
  setArtistMonitor,
  refreshTrackedArtist,
  searchMissingArtist,
  refreshAllTracked,
} from "../../api/client";
import type {
  MonitorMode,
  StartJobResponse,
  TrackedArtist,
} from "../../api/types";

// ─── Constants ───────────────────────────────────────────────────────

const MONITOR_MODES: MonitorMode[] = ["all", "future", "none"];

const MONITOR_MODE_LABELS: Record<MonitorMode, string> = {
  all: "All releases",
  future: "Future only",
  none: "Not monitored",
};

const SELECT_CLASS =
  "rounded-lg border border-slate-700 bg-slate-800 px-2.5 py-1.5 text-xs text-white focus:border-purple-500 focus:outline-none focus:ring-1 focus:ring-purple-500 disabled:cursor-not-allowed disabled:opacity-50";

// ─── Helpers ─────────────────────────────────────────────────────────

function errMessage(err: unknown, fallback: string): string {
  return err instanceof Error ? err.message : fallback;
}

function formatDateTime(iso: string): string {
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : date.toLocaleString();
}

/**
 * Mirrors the job-start handling used by the scan/enrich hooks: publish the
 * returned job into the shared job query and report the outcome. A request
 * for a job that is already running returns `started: false`.
 */
function notifyStarted(
  queryClient: QueryClient,
  res: StartJobResponse,
  label: string,
): void {
  queryClient.setQueryData(["jobs", "current"], res.job);
  if (!res.started) {
    toast.info("Another background job is already running");
    return;
  }
  if (res.job.state === "running") {
    toast.info(`${label} started`);
    return;
  }
  if (res.job.state === "completed") {
    toast.success(res.job.message || `${label} complete`);
  } else if (res.job.state === "failed") {
    toast.error(`Job failed: ${res.job.error ?? "unknown error"}`);
  } else if (res.job.state === "cancelled") {
    toast.info("Job cancelled");
  }
}

/**
 * A tracking job (refresh / search-missing) only changes the tracking data
 * once it finishes, so refetch the tracking queries when the shared job
 * transitions out of `running`. Gated on `enabled` so a non-admin never
 * polls the admin-only /api/jobs endpoint.
 */
function useTrackingJobRefresh(enabled: boolean): void {
  const queryClient = useQueryClient();
  const { data: job } = useJobState(enabled);
  const prevState = useRef<string | undefined>(undefined);

  useEffect(() => {
    const state = job?.state;
    if (prevState.current === "running" && state !== "running") {
      queryClient.invalidateQueries({ queryKey: ["tracking"] });
    }
    prevState.current = state;
  }, [job, queryClient]);
}

// ─── Artist row ──────────────────────────────────────────────────────

interface TrackedArtistRowProps {
  artist: TrackedArtist;
  onSelect: (artistId: number) => void;
}

function TrackedArtistRow({ artist, onSelect }: TrackedArtistRowProps) {
  const queryClient = useQueryClient();

  const invalidateTracking = () => {
    queryClient.invalidateQueries({ queryKey: ["tracking"] });
  };

  const monitor = useMutation({
    mutationFn: (body: { monitored?: boolean; monitor_mode?: MonitorMode }) =>
      setArtistMonitor(artist.id, body),
    onSuccess: invalidateTracking,
    onError: (err) =>
      toast.error(errMessage(err, "Failed to update monitor mode")),
  });

  const refresh = useMutation({
    mutationFn: () => refreshTrackedArtist(artist.id),
    onSuccess: (res) => notifyStarted(queryClient, res, "Refresh"),
    onError: (err) => toast.error(errMessage(err, "Failed to start refresh")),
  });

  const searchMissing = useMutation({
    mutationFn: () => searchMissingArtist(artist.id),
    onSuccess: (res) => notifyStarted(queryClient, res, "Search missing"),
    onError: (err) => toast.error(errMessage(err, "Failed to start search")),
  });

  const busy =
    monitor.isPending || refresh.isPending || searchMissing.isPending;
  const actionError = monitor.error ?? refresh.error ?? searchMissing.error;
  const paused = !artist.monitored || artist.monitor_mode === "none";

  const handleModeChange = (event: ChangeEvent<HTMLSelectElement>) => {
    const mode = event.target.value as MonitorMode;
    // Any non-"none" mode resumes monitoring; mode "none" always wins.
    const monitored = mode !== "none";
    monitor.mutate({ monitor_mode: mode, monitored });
  };

  return (
    <li className="rounded-lg border border-slate-800 bg-slate-900 p-3 transition-colors hover:border-slate-700 sm:p-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between sm:gap-4">
        <div className="min-w-0 flex-1">
          <button
            type="button"
            onClick={() => onSelect(artist.id)}
            className="block max-w-full truncate text-left text-sm font-semibold text-white transition-colors hover:text-purple-400"
            title={artist.name}
          >
            {artist.name}
          </button>
          <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
            <Badge variant="muted" title={artist.provider_artist_id}>
              {artist.provider_name}
            </Badge>
            <Badge variant={paused ? "muted" : "success"}>
              {paused ? "Paused" : "Monitoring"}
            </Badge>
            <span className="text-[11px] text-slate-500">
              {artist.last_refreshed_at
                ? `Refreshed ${formatDateTime(artist.last_refreshed_at)}`
                : "Never refreshed"}
            </span>
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <label
            htmlFor={`monitor-mode-${artist.id}`}
            className="sr-only"
          >
            Monitor mode for {artist.name}
          </label>
          <select
            id={`monitor-mode-${artist.id}`}
            value={artist.monitor_mode}
            onChange={handleModeChange}
            disabled={busy}
            className={SELECT_CLASS}
          >
            {MONITOR_MODES.map((mode) => (
              <option key={mode} value={mode}>
                {MONITOR_MODE_LABELS[mode]}
              </option>
            ))}
          </select>
          <Button
            variant="ghost"
            size="sm"
            loading={refresh.isPending}
            disabled={busy}
            onClick={() => refresh.mutate()}
          >
            <RefreshCw size={14} /> Refresh
          </Button>
          <Button
            variant="ghost"
            size="sm"
            loading={searchMissing.isPending}
            disabled={busy}
            onClick={() => searchMissing.mutate()}
          >
            <Search size={14} /> Search missing
          </Button>
        </div>
      </div>

      {actionError && (
        <StatusMessage
          variant="error"
          className="mt-3"
          message={errMessage(actionError, "Action failed")}
        />
      )}
    </li>
  );
}

// ─── Empty state ─────────────────────────────────────────────────────

function EmptyTrackingState() {
  return (
    <div className="flex flex-col items-center justify-center gap-3 rounded-lg border border-dashed border-slate-800 py-16 text-center">
      <Radio size={32} className="text-slate-600" />
      <div>
        <p className="text-sm font-medium text-white">No tracked artists yet</p>
        <p className="mt-1 text-sm text-slate-500">
          Track an artist from Discover to watch for new releases.
        </p>
      </div>
      <Link
        to="/discover"
        className="mt-1 inline-flex items-center gap-2 rounded-lg bg-purple-600 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-purple-700 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-purple-500 focus-visible:ring-offset-2 focus-visible:ring-offset-slate-950"
      >
        Go to Discover
      </Link>
    </div>
  );
}

// ─── Page ────────────────────────────────────────────────────────────

export default function TrackingPage() {
  const [selectedArtistId, setSelectedArtistId] = useState<number | null>(null);
  const queryClient = useQueryClient();
  const { isAdmin } = useAuth();
  useTrackingJobRefresh(isAdmin);

  // Tracking is an admin-only surface. The route is gated in the nav/router,
  // but gate the queries here too so a direct URL hit (or the brief window
  // before the redirect) never fires the admin-only tracking endpoints.
  const artistsQuery = useQuery({
    queryKey: ["tracking", "artists"],
    queryFn: listTrackedArtists,
    enabled: isAdmin,
  });

  const wantedQuery = useQuery({
    queryKey: ["tracking", "wanted"],
    queryFn: listAllWanted,
    enabled: isAdmin,
  });

  const refreshAll = useMutation({
    mutationFn: refreshAllTracked,
    onSuccess: (res) => notifyStarted(queryClient, res, "Refresh all"),
    onError: (err) =>
      toast.error(errMessage(err, "Failed to start refresh")),
  });

  if (selectedArtistId !== null) {
    return (
      <TrackingArtistDetailView
        artistId={selectedArtistId}
        onBack={() => setSelectedArtistId(null)}
      />
    );
  }

  const artists = artistsQuery.data ?? [];
  const wantedCount = wantedQuery.data?.length ?? 0;

  return (
    <div className="flex flex-col gap-0">
      {/* Header */}
      <div className="mb-6 flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-bold text-white">Tracking</h1>
          <p className="mt-1 text-sm text-slate-400">
            Artists monitored for new and missing releases.
          </p>
        </div>
        <div className="flex items-center gap-2">
          {wantedCount > 0 && (
            <Badge
              variant="warning"
              title="Albums wanted across all tracked artists"
            >
              {wantedCount} wanted
            </Badge>
          )}
          <Button
            variant="primary"
            size="default"
            loading={refreshAll.isPending}
            disabled={refreshAll.isPending || artists.length === 0}
            onClick={() => refreshAll.mutate()}
          >
            <RefreshCw size={14} /> Refresh all
          </Button>
        </div>
      </div>

      {/* Artist list */}
      {artistsQuery.isLoading ? (
        <div className="flex items-center justify-center py-12">
          <Spinner size="md" />
        </div>
      ) : artistsQuery.isError ? (
        <StatusMessage
          variant="error"
          message={errMessage(
            artistsQuery.error,
            "Failed to load tracked artists.",
          )}
        />
      ) : artists.length === 0 ? (
        <EmptyTrackingState />
      ) : (
        <ul className="flex flex-col gap-2">
          {artists.map((artist) => (
            <TrackedArtistRow
              key={artist.id}
              artist={artist}
              onSelect={setSelectedArtistId}
            />
          ))}
        </ul>
      )}
    </div>
  );
}
