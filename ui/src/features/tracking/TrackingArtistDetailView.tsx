import type { ChangeEvent } from "react";
import {
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import type { QueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { toast } from "sonner";
import {
  ArrowLeft,
  Eye,
  EyeOff,
  Library,
  RefreshCw,
  Search,
  Trash2,
} from "lucide-react";
import Spinner from "../../components/Spinner";
import StatusMessage from "../../components/StatusMessage";
import Button from "../../components/Button";
import Badge from "../../components/Badge";
import {
  deleteTrackedArtist,
  getTrackedArtist,
  refreshTrackedArtist,
  searchMissingArtist,
  setAlbumMonitored,
  setAlbumStatus,
  setArtistMonitor,
} from "../../api/client";
import type {
  AlbumStatus,
  MonitorMode,
  StartJobResponse,
  TrackedAlbum,
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

interface StatusBadgeDef {
  label: string;
  className: string;
}

const STATUS_BADGES: Record<AlbumStatus, StatusBadgeDef> = {
  wanted: {
    label: "Wanted",
    className: "border-amber-700 bg-amber-900/50 text-amber-300",
  },
  downloading: {
    label: "Downloading",
    className: "border-blue-700 bg-blue-900/50 text-blue-300",
  },
  downloaded: {
    label: "Downloaded",
    className: "border-green-800 bg-green-900/50 text-green-400",
  },
  ignored: {
    label: "Ignored",
    className: "border-slate-700 bg-slate-800 text-slate-400",
  },
};

// ─── Helpers ─────────────────────────────────────────────────────────

function errMessage(err: unknown, fallback: string): string {
  return err instanceof Error ? err.message : fallback;
}

function formatDateTime(iso: string): string {
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : date.toLocaleString();
}

function capitalize(value: string): string {
  return value.length === 0 ? value : value.charAt(0).toUpperCase() + value.slice(1);
}

/** See TrackingPage.notifyStarted — duplicated so each file stays self-contained. */
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

// ─── Small shared controls ───────────────────────────────────────────

interface ToggleSwitchProps {
  checked: boolean;
  disabled?: boolean;
  onToggle: () => void;
  label: string;
}

function ToggleSwitch({
  checked,
  disabled = false,
  onToggle,
  label,
}: ToggleSwitchProps) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={onToggle}
      className={`relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors disabled:cursor-not-allowed disabled:opacity-50 ${
        checked ? "bg-purple-600" : "bg-slate-700"
      }`}
    >
      <span
        className={`inline-block h-3.5 w-3.5 rounded-full bg-white transition-transform ${
          checked ? "translate-x-[18px]" : "translate-x-1"
        }`}
      />
    </button>
  );
}

function BackButton({ onClick }: { onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="flex h-9 w-9 items-center justify-center rounded-lg border border-slate-800 bg-slate-900 text-slate-400 transition-colors hover:border-slate-700 hover:text-white"
      title="Back to tracked artists"
    >
      <ArrowLeft size={18} />
    </button>
  );
}

function StatusBadge({ status }: { status: AlbumStatus }) {
  const def = STATUS_BADGES[status];
  return (
    <span
      className={`inline-flex items-center rounded border px-2 py-0.5 text-xs font-semibold ${def.className}`}
    >
      {def.label}
    </span>
  );
}

// ─── Album row ───────────────────────────────────────────────────────

interface TrackingAlbumRowProps {
  album: TrackedAlbum;
  artistLibraryId: number | undefined;
}

function TrackingAlbumRow({ album, artistLibraryId }: TrackingAlbumRowProps) {
  const queryClient = useQueryClient();

  const invalidateTracking = () => {
    queryClient.invalidateQueries({ queryKey: ["tracking"] });
  };

  const monitor = useMutation({
    mutationFn: (monitored: boolean) => setAlbumMonitored(album.id, monitored),
    onSuccess: invalidateTracking,
    onError: (err) =>
      toast.error(errMessage(err, "Failed to update album")),
  });

  const statusMutation = useMutation({
    mutationFn: (status: "wanted" | "ignored") =>
      setAlbumStatus(album.id, status),
    onSuccess: invalidateTracking,
    onError: (err) =>
      toast.error(errMessage(err, "Failed to update album status")),
  });

  const inLibrary = album.library_album_id != null;
  const ignored = album.status === "ignored";
  const busy = monitor.isPending || statusMutation.isPending;
  const libraryHref =
    artistLibraryId != null && album.library_album_id != null
      ? `/library?artist=${artistLibraryId}&album=${album.library_album_id}`
      : null;

  return (
    <li
      className={`flex flex-col gap-2 rounded-lg border p-3 transition-colors sm:flex-row sm:items-center sm:gap-4 ${
        ignored
          ? "border-slate-800/70 bg-slate-900/30 opacity-70"
          : "border-slate-800 bg-slate-900/50"
      }`}
    >
      <div className="min-w-0 flex-1">
        <p
          className={`truncate text-sm font-medium ${
            ignored ? "text-slate-400 line-through" : "text-white"
          }`}
          title={album.title}
        >
          {album.title}
        </p>
        <p className="mt-0.5 text-xs text-slate-500">
          {album.album_type ? capitalize(album.album_type) : "Album"}
          {album.year ? ` · ${album.year}` : ""}
        </p>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <StatusBadge status={album.status} />

        {libraryHref ? (
          <Link
            to={libraryHref}
            title="Open in Library"
            className="inline-flex items-center gap-1 rounded border border-green-800 bg-green-900/50 px-2 py-0.5 text-xs font-semibold text-green-400 transition-colors hover:bg-green-900"
          >
            <Library size={12} /> In library
          </Link>
        ) : (
          <Badge
            variant={inLibrary ? "success" : "muted"}
            title={inLibrary ? "In library" : "Not yet matched to a library album"}
          >
            {inLibrary ? "In library" : "Not in library"}
          </Badge>
        )}

        <label className="flex items-center gap-2 text-xs text-slate-400">
          <ToggleSwitch
            checked={album.monitored}
            disabled={busy}
            onToggle={() => monitor.mutate(!album.monitored)}
            label={`Monitor ${album.title}`}
          />
          {album.monitored ? "Monitored" : "Unmonitored"}
        </label>

        <Button
          variant="ghost"
          size="sm"
          loading={statusMutation.isPending}
          disabled={busy}
          title={ignored ? "Restore to wanted" : "Ignore this album"}
          onClick={() =>
            statusMutation.mutate(ignored ? "wanted" : "ignored")
          }
        >
          {ignored ? <Eye size={14} /> : <EyeOff size={14} />}
          {ignored ? "Unignore" : "Ignore"}
        </Button>
      </div>
    </li>
  );
}

// ─── Detail view ─────────────────────────────────────────────────────

interface TrackingArtistDetailViewProps {
  artistId: number;
  onBack: () => void;
}

export default function TrackingArtistDetailView({
  artistId,
  onBack,
}: TrackingArtistDetailViewProps) {
  const queryClient = useQueryClient();

  const artistQuery = useQuery({
    queryKey: ["tracking", "artist", artistId],
    queryFn: () => getTrackedArtist(artistId),
  });

  const invalidateTracking = () => {
    queryClient.invalidateQueries({ queryKey: ["tracking"] });
  };

  const monitor = useMutation({
    mutationFn: (body: { monitored?: boolean; monitor_mode?: MonitorMode }) =>
      setArtistMonitor(artistId, body),
    onSuccess: invalidateTracking,
    onError: (err) =>
      toast.error(errMessage(err, "Failed to update monitor mode")),
  });

  const refresh = useMutation({
    mutationFn: () => refreshTrackedArtist(artistId),
    onSuccess: (res) => {
      notifyStarted(queryClient, res, "Refresh");
      invalidateTracking();
    },
    onError: (err) => toast.error(errMessage(err, "Failed to start refresh")),
  });

  const searchMissing = useMutation({
    mutationFn: () => searchMissingArtist(artistId),
    onSuccess: (res) => {
      notifyStarted(queryClient, res, "Search missing");
      invalidateTracking();
    },
    onError: (err) => toast.error(errMessage(err, "Failed to start search")),
  });

  const remove = useMutation({
    mutationFn: () => deleteTrackedArtist(artistId),
    onSuccess: () => {
      toast.success("Artist removed from tracking");
      invalidateTracking();
      onBack();
    },
    onError: (err) => toast.error(errMessage(err, "Failed to remove artist")),
  });

  if (artistQuery.isLoading) {
    return (
      <div className="flex flex-col gap-0">
        <div className="mb-6 flex items-center gap-3">
          <BackButton onClick={onBack} />
          <div className="flex items-center gap-2">
            <Spinner size="sm" />
            <span className="text-sm text-slate-400">Loading…</span>
          </div>
        </div>
        <div className="flex items-center justify-center py-12">
          <Spinner size="md" />
        </div>
      </div>
    );
  }

  const data = artistQuery.data;

  if (artistQuery.isError || !data) {
    return (
      <div className="flex flex-col gap-0">
        <div className="mb-6">
          <BackButton onClick={onBack} />
        </div>
        <StatusMessage
          variant="error"
          message={errMessage(
            artistQuery.error,
            "Failed to load tracked artist.",
          )}
        />
      </div>
    );
  }

  const { artist, albums } = data;
  const headerBusy =
    monitor.isPending ||
    refresh.isPending ||
    searchMissing.isPending ||
    remove.isPending;
  const actionError =
    monitor.error ?? refresh.error ?? searchMissing.error ?? remove.error;
  const active = artist.monitored && artist.monitor_mode !== "none";

  const handleModeChange = (event: ChangeEvent<HTMLSelectElement>) => {
    const mode = event.target.value as MonitorMode;
    // Any non-"none" mode resumes monitoring; mode "none" always wins.
    const monitored = mode !== "none";
    monitor.mutate({ monitor_mode: mode, monitored });
  };

  const handleDelete = () => {
    const confirmed = window.confirm(
      `Stop tracking ${artist.name}? Its discovered albums will be removed.`,
    );
    if (!confirmed) return;
    remove.mutate();
  };

  return (
    <div className="flex flex-col gap-0">
      {/* Header */}
      <div className="mb-6">
        <div className="mb-4 flex items-center justify-between gap-3">
          <BackButton onClick={onBack} />
          <Button
            variant="danger"
            size="sm"
            loading={remove.isPending}
            onClick={handleDelete}
          >
            <Trash2 size={14} /> Remove
          </Button>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <h1 className="text-xl font-bold text-white">{artist.name}</h1>
          <Badge variant="muted" title={artist.provider_artist_id}>
            {artist.provider_name}
          </Badge>
          {artist.library_artist_id != null && (
            <Badge variant="success">In library</Badge>
          )}
        </div>

        <div className="mt-3 flex flex-wrap items-center gap-3">
          <label className="flex items-center gap-2 text-sm text-slate-300">
            <ToggleSwitch
              checked={active}
              disabled={headerBusy || artist.monitor_mode === "none"}
              onToggle={() => monitor.mutate({ monitored: !artist.monitored })}
              label="Monitored"
            />
            {active ? "Monitored" : "Paused"}
          </label>

          <label htmlFor="detail-monitor-mode" className="sr-only">
            Monitor mode
          </label>
          <select
            id="detail-monitor-mode"
            value={artist.monitor_mode}
            onChange={handleModeChange}
            disabled={headerBusy}
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
            disabled={headerBusy}
            onClick={() => refresh.mutate()}
          >
            <RefreshCw size={14} /> Refresh
          </Button>
          <Button
            variant="ghost"
            size="sm"
            loading={searchMissing.isPending}
            disabled={headerBusy}
            onClick={() => searchMissing.mutate()}
          >
            <Search size={14} /> Search missing
          </Button>
        </div>

        <p className="mt-2 text-xs text-slate-500">
          {artist.last_refreshed_at
            ? `Last refreshed ${formatDateTime(artist.last_refreshed_at)}`
            : "Never refreshed"}
        </p>

        {actionError && (
          <StatusMessage
            variant="error"
            className="mt-3"
            message={errMessage(actionError, "Action failed")}
          />
        )}
      </div>

      {/* Albums */}
      <section>
        <h2 className="mb-3 text-sm font-semibold uppercase tracking-wider text-slate-400">
          Albums ({albums.length})
        </h2>
        {albums.length === 0 ? (
          <p className="rounded-lg border border-dashed border-slate-800 py-10 text-center text-sm text-slate-500">
            No albums discovered yet. Run Refresh to look for releases.
          </p>
        ) : (
          <ul className="flex flex-col gap-2">
            {albums.map((album) => (
              <TrackingAlbumRow
                key={album.id}
                album={album}
                artistLibraryId={artist.library_artist_id}
              />
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}
