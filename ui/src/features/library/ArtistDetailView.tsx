import { useState, useCallback, useMemo, useRef } from "react";
import { useNavigate } from "react-router-dom";
import { ArrowLeft, Music, Search, Disc3, Library, Download } from "lucide-react";
import { toast } from "sonner";
import Spinner from "../../components/Spinner";
import StatusMessage from "../../components/StatusMessage";
import { useDiscoveryResolveArtist, useArtistOverview } from "../../hooks/use-discovery";
import { downloadBest } from "../../api/client";
import type { Artist, Album, Track } from "../../api/types";

interface ArtistDetailViewProps {
  artist: Artist | undefined;
  albums: Album[];
  tracks: Track[];
  isLoadingArtist: boolean;
  isLoadingAlbums: boolean;
  isLoadingTracks: boolean;
  onBack: () => void;
  onSelectAlbum: (albumId: number) => void;
}

/** Build album-title lookup map from albums list. */
function buildAlbumMap(albums: Album[]): Map<number, string> {
  const map = new Map<number, string>();
  for (const a of albums) {
    map.set(a.id, a.title);
  }
  return map;
}

/** Format file size to human-readable string. */
function formatSize(bytes?: number): string {
  if (bytes == null) return "—";
  if (bytes < 1024) return `${bytes} B`;
  const kb = bytes / 1024;
  if (kb < 1024) return `${kb.toFixed(1)} KB`;
  const mb = kb / 1024;
  if (mb < 1024) return `${mb.toFixed(1)} MB`;
  const gb = mb / 1024;
  return `${gb.toFixed(1)} GB`;
}

function AlbumCard({ album, onSelect }: { album: Album; onSelect: (id: number) => void }) {
  const [imgError, setImgError] = useState(false);

  return (
    <button
      type="button"
      onClick={() => onSelect(album.id)}
      className="cursor-pointer rounded-lg border border-slate-800 bg-slate-900 text-left transition-colors hover:border-slate-700"
    >
      <div className="relative aspect-square w-full overflow-hidden rounded-t-lg bg-slate-800">
        {!imgError ? (
          <img
            src={`/api/covers/${album.id}`}
            alt={album.title}
            loading="lazy"
            onError={() => setImgError(true)}
            className="h-full w-full object-cover"
          />
        ) : (
          <div className="flex h-full w-full items-center justify-center">
            <Music size={36} className="text-slate-600" />
          </div>
        )}
      </div>
      <div className="p-3">
        <p className="truncate text-sm font-medium text-white" title={album.title}>
          {album.title}
        </p>
        {album.year && (
          <p className="mt-1 text-[11px] text-slate-500">{album.year}</p>
        )}
      </div>
    </button>
  );
}

function ArtistHeader({ artist, albums }: { artist: Artist; albums: Album[] }) {
  const navigate = useNavigate();
  const resolveMutation = useDiscoveryResolveArtist();
  const resolvingRef = useRef(false);
  const [resolving, setResolving] = useState(false);
  const [downloading, setDownloading] = useState(false);
  const { data: overview, isLoading: isLoadingOverview } = useArtistOverview(artist.name);

  const handleDownloadTopTracks = useCallback(async () => {
    if (!overview?.top_tracks.length) return;
    const total = overview.top_tracks.length;
    setDownloading(true);

    // Fire in batches of 3 to avoid rate-limit contention.
    const results: PromiseSettledResult<unknown>[] = [];
    for (let i = 0; i < overview.top_tracks.length; i += 3) {
      const batch = overview.top_tracks.slice(i, i + 3).map((t) =>
        downloadBest({
          title: t.title,
          artist: t.artist_name,
          album: t.album_title,
          duration: t.duration_ms,
        }),
      );
      const batchResults = await Promise.allSettled(batch);
      results.push(...batchResults);
    }
    setDownloading(false);
    const queued = results.filter((r) => r.status === "fulfilled").length;
    const errors: string[] = [];
    results.forEach((r, i) => {
      if (r.status === "rejected") {
        const t = overview.top_tracks[i];
        const msg = r.reason instanceof Error ? r.reason.message : "failed";
        errors.push(`${t.artist_name} - ${t.title}: ${msg}`);
      }
    });
    if (queued > 0) toast.success(`${queued}/${total} tracks queued`);
    errors.slice(0, 3).forEach((e) => toast.error(e));
  }, [overview]);

  const handleDiscover = useCallback(() => {
    if (resolvingRef.current) return;
    resolvingRef.current = true;
    setResolving(true);
    resolveMutation.mutate(artist.name, {
      onSuccess: (resolved) => {
        navigate(
          `/discover?artist_id=${encodeURIComponent(resolved.provider_id)}&provider=${encodeURIComponent(resolved.provider_name ?? "")}&artist_name=${encodeURIComponent(resolved.name)}`,
        );
      },
      onError: () => {
        navigate(`/discover?q=${encodeURIComponent(artist.name)}`);
      },
      onSettled: () => {
        resolvingRef.current = false;
        setResolving(false);
      },
    });
  }, [artist.name, navigate, resolveMutation]);

  const localByType = useMemo(() => {
    const counts: Record<string, number> = {};
    for (const a of albums ?? []) {
      const t = a.album_type || "album";
      counts[t] = (counts[t] || 0) + 1;
    }
    return counts;
  }, [albums]);

  const discOrder = ["album", "single", "ep", "compilation", "live"];

  return (
    <div className="grid gap-5 sm:grid-cols-[auto_1fr] lg:grid-cols-[auto_1fr_1fr_1fr]">
      {/* Column 1: Name + image + genres */}
      <div className="flex flex-col items-start gap-3">
        <button
          type="button"
          onClick={handleDiscover}
          className="text-left text-xl font-bold text-white transition-colors hover:text-purple-400"
        >
          {artist.name}
        </button>

        <button
          type="button"
          onClick={handleDiscover}
          disabled={resolving}
          className="group relative shrink-0 overflow-hidden rounded-lg disabled:opacity-60"
          title={`Search "${artist.name}" in Discovery`}
        >
          {artist.thumb_url ? (
            <img
              src={artist.thumb_url}
              alt={artist.name}
              className="h-28 w-28 object-cover transition-transform group-hover:scale-105 sm:h-32 sm:w-32"
            />
          ) : (
            <div className="flex h-28 w-28 items-center justify-center rounded-lg bg-slate-800 sm:h-32 sm:w-32">
              <Music size={36} className="text-slate-600" />
            </div>
          )}
          <div className="absolute inset-0 flex items-center justify-center bg-black/50 opacity-0 transition-opacity group-hover:opacity-100">
            {resolving ? (
              <Spinner size="sm" />
            ) : (
              <Search size={22} className="text-white" />
            )}
          </div>
        </button>

        {artist.genres && artist.genres.length > 0 && (
          <p className="text-sm text-slate-400">
            {artist.genres.join(", ")}
          </p>
        )}
        {artist.summary && (
          <p className="line-clamp-3 text-sm leading-relaxed text-slate-500">
            {artist.summary}
          </p>
        )}
      </div>

      {/* Column 2: Top Tracks (hidden on mobile, shown lg+) */}
      <div className="hidden lg:block">
        <h3 className="mb-2 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-slate-500">
          <Disc3 size={12} /> Top Tracks
        </h3>
        {isLoadingOverview ? null : overview && overview.top_tracks.length > 0 ? (
          <ol className="space-y-1">
            {overview.top_tracks.map((track, i) => (
              <li key={track.provider_id} className="flex items-center gap-2 text-xs">
                <span className="w-4 text-right tabular-nums text-slate-600">
                  {i + 1}
                </span>
                <span className="min-w-0 flex-1 truncate text-slate-300">
                  {track.title}
                </span>
                <span className="shrink-0 tabular-nums text-slate-600">
                  {formatDuration(track.duration_ms)}
                </span>
              </li>
            ))}
          </ol>
        ) : null}
        {overview && overview.top_tracks.length > 0 && (
          <button
            type="button"
            onClick={handleDownloadTopTracks}
            disabled={downloading}
            className="mt-2 flex w-full items-center justify-center gap-1.5 rounded-md bg-purple-600/20 px-2 py-1.5 text-[11px] font-medium text-purple-400 transition-colors hover:bg-purple-600/30 disabled:opacity-50"
          >
            <Download size={12} />
            {downloading ? "Queuing…" : "Download Top Tracks"}
          </button>
        )}
      </div>

      {/* Column 3: Discography stats (hidden on mobile, shown lg+) */}
      <div className="hidden lg:block">
        <h3 className="mb-2 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-slate-500">
          <Library size={12} /> Discography
        </h3>
        {isLoadingOverview ? null : overview ? (
          <div className="space-y-1.5">
            {discOrder.map((type) => {
              const discoCount = overview.discography[type];
              if (!discoCount) return null;
              const localCount = localByType[type] || 0;
              const pct = Math.min(100, (localCount / discoCount) * 100);
              return (
                <div key={type}>
                  <div className="flex items-center justify-between text-xs">
                    <span className="capitalize text-slate-400">{type}s</span>
                    <span className="tabular-nums">
                      <span className="text-purple-400">{localCount}</span>
                      <span className="text-slate-600">/{discoCount}</span>
                    </span>
                  </div>
                  <div className="mt-0.5 h-1 overflow-hidden rounded-full bg-slate-800">
                    <div className="h-full rounded-full bg-purple-600 transition-all" style={{ width: `${pct}%` }} />
                  </div>
                </div>
              );
            })}
            {Object.entries(overview.discography)
              .filter(([t]) => !discOrder.includes(t))
              .map(([type, discoCount]) => {
                const localCount = localByType[type] || 0;
                const pct = Math.min(100, (localCount / discoCount) * 100);
                return (
                  <div key={type}>
                    <div className="flex items-center justify-between text-xs">
                      <span className="capitalize text-slate-400">{type}s</span>
                      <span className="tabular-nums">
                        <span className="text-purple-400">{localCount}</span>
                        <span className="text-slate-600">/{discoCount}</span>
                      </span>
                    </div>
                    <div className="mt-0.5 h-1 overflow-hidden rounded-full bg-slate-800">
                      <div className="h-full rounded-full bg-purple-600 transition-all" style={{ width: `${pct}%` }} />
                    </div>
                  </div>
                );
              })}
          </div>
        ) : null}
      </div>

      {/* Mobile/tablet: overview section below image (sm-md only) */}
      {overview && (
        <div className="grid gap-4 rounded-lg border border-slate-800 bg-slate-900/50 p-4 sm:grid-cols-2 lg:hidden sm:col-span-2">
          {/* Top Tracks */}
          <div>
            <h3 className="mb-2 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-slate-500">
              <Disc3 size={12} /> Top Tracks
            </h3>
            {overview.top_tracks.length > 0 ? (
              <ol className="space-y-1">
            {overview.top_tracks.map((track, i) => (
                  <li key={track.provider_id} className="flex items-center gap-2 text-xs">
                    <span className="w-4 text-right tabular-nums text-slate-600">{i + 1}</span>
                    <span className="min-w-0 flex-1 truncate text-slate-300">{track.title}</span>
                    <span className="shrink-0 tabular-nums text-slate-600">{formatDuration(track.duration_ms)}</span>
                  </li>
                ))}
              </ol>
            ) : (
              <p className="py-2 text-xs text-slate-600">No top tracks available</p>
            )}
            {overview.top_tracks.length > 0 && (
              <button
                type="button"
                onClick={handleDownloadTopTracks}
                disabled={downloading}
                className="mt-2 flex w-full items-center justify-center gap-1.5 rounded-md bg-purple-600/20 px-2 py-1.5 text-[11px] font-medium text-purple-400 transition-colors hover:bg-purple-600/30 disabled:opacity-50"
              >
                <Download size={12} />
                {downloading ? "Queuing…" : "Download Top Tracks"}
              </button>
            )}
          </div>
          {/* Discography */}
          <div>
            <h3 className="mb-2 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-slate-500">
              <Library size={12} /> Discography
            </h3>
            <div className="space-y-1.5">
              {discOrder.map((type) => {
                const discoCount = overview.discography[type];
                if (!discoCount) return null;
                const localCount = localByType[type] || 0;
                const pct = Math.min(100, (localCount / discoCount) * 100);
                return (
                  <div key={type}>
                    <div className="flex items-center justify-between text-xs">
                      <span className="capitalize text-slate-400">{type}s</span>
                      <span className="tabular-nums">
                        <span className="text-purple-400">{localCount}</span>
                        <span className="text-slate-600">/{discoCount}</span>
                      </span>
                    </div>
                    <div className="mt-0.5 h-1 overflow-hidden rounded-full bg-slate-800">
                      <div className="h-full rounded-full bg-purple-600 transition-all" style={{ width: `${pct}%` }} />
                    </div>
                  </div>
                );
              })}
              {Object.entries(overview.discography)
                .filter(([t]) => !discOrder.includes(t))
                .map(([type, discoCount]) => {
                  const localCount = localByType[type] || 0;
                  const pct = Math.min(100, (localCount / discoCount) * 100);
                  return (
                    <div key={type}>
                      <div className="flex items-center justify-between text-xs">
                        <span className="capitalize text-slate-400">{type}s</span>
                        <span className="tabular-nums">
                          <span className="text-purple-400">{localCount}</span>
                          <span className="text-slate-600">/{discoCount}</span>
                        </span>
                      </div>
                      <div className="mt-0.5 h-1 overflow-hidden rounded-full bg-slate-800">
                        <div className="h-full rounded-full bg-purple-600 transition-all" style={{ width: `${pct}%` }} />
                      </div>
                    </div>
                  );
                })}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

/** Format milliseconds to m:ss string. */
function formatDuration(ms: number): string {
  if (!Number.isFinite(ms)) return "—";
  const m = Math.floor(ms / 60000);
  const s = Math.floor((ms % 60000) / 1000);
  return `${m}:${s.toString().padStart(2, "0")}`;
}

export default function ArtistDetailView({
  artist,
  albums,
  tracks,
  isLoadingArtist,
  isLoadingAlbums,
  isLoadingTracks,
  onBack,
  onSelectAlbum,
}: ArtistDetailViewProps) {
  const albumMap = buildAlbumMap(albums);

  return (
    <div className="flex flex-col gap-0">
      {/* Header with back button, artist image, and info */}
      <div className="mb-6">
        {/* Back button row */}
        <div className="mb-4">
          <button
            type="button"
            onClick={onBack}
            className="flex h-9 w-9 items-center justify-center rounded-lg border border-slate-800 bg-slate-900 text-slate-400 transition-colors hover:border-slate-700 hover:text-white"
            title="Back to artists"
          >
            <ArrowLeft size={18} />
          </button>
        </div>

        {isLoadingArtist ? (
          <div className="flex items-center gap-2">
            <Spinner size="sm" />
            <span className="text-sm text-slate-400">Loading…</span>
          </div>
        ) : artist ? (
          <ArtistHeader artist={artist} albums={albums} />
        ) : (
          <StatusMessage variant="error" message="Artist not found." />
        )}
      </div>

      {/* Albums section */}
      <section className="mb-6">
        <h2 className="mb-3 text-sm font-semibold uppercase tracking-wider text-slate-400">
          Albums
        </h2>
        {isLoadingAlbums ? (
          <div className="flex items-center justify-center py-8">
            <Spinner size="md" />
          </div>
        ) : albums.length === 0 ? (
          <p className="py-8 text-center text-sm text-slate-500">No albums found.</p>
        ) : (
          <div className="grid grid-cols-[repeat(auto-fill,minmax(160px,1fr))] gap-4">
            {albums.map((album) => (
              <AlbumCard key={album.id} album={album} onSelect={onSelectAlbum} />
            ))}
          </div>
        )}
      </section>

      {/* Tracks section */}
      <section>
        <h2 className="mb-3 text-sm font-semibold uppercase tracking-wider text-slate-400">
          Tracks
        </h2>
        {isLoadingTracks ? (
          <div className="flex items-center justify-center py-8">
            <Spinner size="md" />
          </div>
        ) : tracks.length === 0 ? (
          <p className="py-8 text-center text-sm text-slate-500">No tracks found.</p>
        ) : (
          <div className="overflow-x-auto rounded-lg border border-slate-800">
            <table className="w-full text-left text-sm">
              <thead>
                <tr className="border-b border-slate-800">
                  <th className="w-10 px-4 py-3 text-xs font-semibold uppercase tracking-wider text-slate-400">
                    #
                  </th>
                  <th className="px-4 py-3 text-xs font-semibold uppercase tracking-wider text-slate-400">
                    Title
                  </th>
                  <th className="hidden px-4 py-3 text-xs font-semibold uppercase tracking-wider text-slate-400 sm:table-cell">
                    Album
                  </th>
                  <th className="w-24 px-4 py-3 text-right text-xs font-semibold uppercase tracking-wider text-slate-400">
                    Size
                  </th>
                </tr>
              </thead>
              <tbody>
                {tracks.map((track, idx) => (
                  <tr
                    key={track.id}
                    className="border-b border-slate-800/50 transition-colors hover:bg-slate-800/30"
                  >
                    <td className="px-4 py-3 text-xs text-slate-500 tabular-nums">
                      {idx + 1}
                    </td>
                    <td className="px-4 py-3 font-medium text-slate-200">
                      <span className="line-clamp-1">{track.title}</span>
                    </td>
                    <td className="hidden px-4 py-3 sm:table-cell">
                      <span className="block max-w-xs truncate text-xs text-slate-500">
                        {albumMap.get(track.album_id) || "—"}
                      </span>
                    </td>
                    <td className="px-4 py-3 text-right text-xs text-slate-500 tabular-nums">
                      {formatSize(track.file_size)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </div>
  );
}
