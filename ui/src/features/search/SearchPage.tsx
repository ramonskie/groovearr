import { useState, useCallback } from "react";
import { useNavigate } from "react-router-dom";
import { useSearch } from "../../hooks/use-search";
import { useStartDownload, useStartBestDownload } from "../../hooks/use-downloads";
import { useSources } from "../../hooks/use-config";
import { toast } from "sonner";
import type { AlbumResult, TrackResult } from "../../api/types";
import SearchForm from "./SearchForm";
import TrackResults from "./TrackResults";
import AlbumResults from "./AlbumResults";
import DownloadConfirmDialog from "./DownloadConfirmDialog";
import Card from "../../components/Card";
import StatusMessage from "../../components/StatusMessage";
import Spinner from "../../components/Spinner";

function SearchPage() {
  // ── State ──────────────────────────────────────────────────────────
  const [query, setQuery] = useState("");
  const [source, setSource] = useState("");
  const [dialogAlbum, setDialogAlbum] = useState<AlbumResult | null>(null);

  // ── Hooks ──────────────────────────────────────────────────────────
  const navigate = useNavigate();
  const searchMutation = useSearch();
  const startDownload = useStartDownload();
  const startBestDownload = useStartBestDownload();
  const { data: sources } = useSources();

  // ── Handlers ───────────────────────────────────────────────────────
  const handleSearch = useCallback(() => {
    if (!query.trim()) return;
    searchMutation.mutate({
      query: query.trim(),
      ...(source ? { source } : {}),
    });
  }, [query, source, searchMutation]);

  const handleTrackDownload = useCallback(
    (track: TrackResult) => {
      const downloadSource = source || "";

      startDownload.mutate(
        {
          source: downloadSource,
          username: track.username,
          filename: track.filename,
          size: track.size,
          bitrate: track.bitrate,
          quality: track.quality,
        },
        {
          onSuccess: (data) => {
            toast.success(`Download started: ${track.title || track.filename}`, {
              description: `ID: ${data.download_id}`,
            });
            navigate("/downloads");
          },
          onError: (err) => {
            toast.error("Download failed", {
              description: err instanceof Error ? err.message : "Unknown error",
            });
          },
        },
      );
    },
    [source, startDownload, navigate],
  );

  const handleAlbumDownload = useCallback(
    async (_album: AlbumResult, tracks: TrackResult[]) => {
      const results = [];
      for (const track of tracks) {
        const downloadSource = source || "";
        try {
          const result = await startDownload.mutateAsync({
            source: downloadSource,
            username: track.username,
            filename: track.filename,
            size: track.size,
            bitrate: track.bitrate,
            quality: track.quality,
          });
          results.push({ status: "fulfilled" as const, value: result });
        } catch (primaryErr) {
          try {
            const fallback = await startBestDownload.mutateAsync({
              title: track.title || track.filename,
              artist: track.artist,
              duration: track.duration,
              exclude_source: downloadSource,
            });
            results.push({ status: "fulfilled" as const, value: fallback });
          } catch {
            results.push({ status: "rejected" as const, reason: primaryErr });
          }
        }
        // Rate-limit: 300ms delay between tracks to avoid hammering APIs
        await new Promise((r) => setTimeout(r, 300));
      }

      const succeeded = results.filter((r) => r.status === "fulfilled").length;
      const failed = results.filter((r) => r.status === "rejected").length;

      if (failed === 0) {
        toast.success(`Downloading album: ${_album.album_title}`, {
          description: `${succeeded} track${succeeded !== 1 ? "s" : ""} queued`,
        });
      } else {
        toast.warning(`Album download incomplete`, {
          description: `${succeeded} queued, ${failed} failed`,
        });
      }
      navigate("/downloads");
    },
    [source, startDownload, startBestDownload, navigate],
  );

  // ── Render ─────────────────────────────────────────────────────────
  const results = searchMutation.data;
  const hasResults =
    results && (results.tracks.length > 0 || results.albums.length > 0);

  return (
    <div className="mx-auto max-w-5xl space-y-6">
      {/* Search Form */}
      <Card>
        <SearchForm
          query={query}
          onQueryChange={setQuery}
          source={source}
          onSourceChange={setSource}
          loading={searchMutation.isPending}
          onSubmit={handleSearch}
        />
      </Card>

      {/* Source status badges */}
      {sources && sources.length > 0 && (
        <div className="flex flex-wrap gap-2">
          {sources.map((s) => {
            // Use capabilities for per-feature status if available, otherwise flat status.
            const caps = s.capabilities;
            const primaryStatus = caps
              ? (Object.values(caps).includes("connected") ? "connected"
                : Object.values(caps).includes("configured") ? "configured"
                : "not_configured")
              : s.status;

            return (
            <span
              key={s.name}
              className={`inline-flex items-center gap-1.5 rounded-full border px-3 py-1 text-xs font-medium ${
                primaryStatus === "connected"
                  ? "border-green-800 bg-green-950/50 text-green-400"
                  : primaryStatus === "configured"
                    ? "border-yellow-800 bg-yellow-950/50 text-yellow-400"
                    : "border-slate-700 bg-slate-900 text-slate-500"
              }`}
            >
              {/* Capability dots */}
              {caps && Object.keys(caps).length > 0 && (
                <span className="flex gap-0.5 mr-0.5">
                  {Object.entries(caps).map(([cap, st]) => (
                    <span
                      key={cap}
                      title={`${cap}: ${st}`}
                      className={`h-1.5 w-1.5 rounded-full ${
                        st === "connected" ? "bg-green-400"
                        : st === "configured" ? "bg-yellow-400"
                        : "bg-slate-600"
                      }`}
                    />
                  ))}
                </span>
              )}
              {s.display_name}
            </span>
            );
          })}
        </div>
      )}
      {/* Status area */}
      {searchMutation.isPending && (
        <StatusMessage variant="info" message="Searching...">
          <Spinner size="sm" className="ml-2 inline-block" />
        </StatusMessage>
      )}

      {searchMutation.isError && (
        <StatusMessage variant="error">
          {searchMutation.error instanceof Error
            ? searchMutation.error.message
            : "Search failed. Please try again."}
        </StatusMessage>
      )}

      {/* Results */}
      {hasResults && (
        <>
          {results.tracks.length > 0 && (
            <Card
              title={`Tracks (${results.tracks.length})`}
              className="overflow-hidden"
            >
              <TrackResults
                tracks={results.tracks}
                onDownload={handleTrackDownload}
              />
            </Card>
          )}

          {results.albums.length > 0 && (
            <Card
              title={`Albums (${results.albums.length})`}
              className="overflow-hidden"
            >
              <AlbumResults
                albums={results.albums}
                onDownload={setDialogAlbum}
              />
            </Card>
          )}
        </>
      )}

      {/* Empty state — shown after successful search with no results */}
      {searchMutation.isSuccess && !hasResults && (
        <StatusMessage variant="info" message="No results found." />
      )}

      {/* Download Confirm Dialog */}
      {dialogAlbum && (
        <DownloadConfirmDialog
          open={dialogAlbum !== null}
          onOpenChange={(open) => {
            if (!open) setDialogAlbum(null);
          }}
          album={dialogAlbum}
          onConfirm={(album, tracks) => {
            setDialogAlbum(null);
            handleAlbumDownload(album, tracks);
          }}
          loading={
            startDownload.isPending || startBestDownload.isPending
          }
        />
      )}
    </div>
  );
}

export default SearchPage;
