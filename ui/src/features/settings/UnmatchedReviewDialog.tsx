import { useMemo, useState } from "react";
import * as Dialog from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  ApiRequestError,
  discoverySearch,
  getAlbumTracks,
  getUnmatchedAlbums,
  getUnmatchedReview,
  matchUnmatchedAlbum,
} from "../../api/client";
import type {
  DiscoveryAlbum,
  DiscoveryTrack,
  UnmatchedAlbum,
  UnmatchedMatchResponse,
  UnmatchedReviewCandidate,
} from "../../api/types";
import Button from "../../components/Button";
import { AlbumRow, LocalTrack } from "./UnmatchedReviewParts";
import UnmatchedSearchSection from "./UnmatchedSearchSection";
import {
  candidateFromSearch,
  candidateKey,
  releaseKey,
} from "./unmatched-review-helpers";

interface UnmatchedReviewDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

export default function UnmatchedReviewDialog({
  open,
  onOpenChange,
}: UnmatchedReviewDialogProps) {
  const queryClient = useQueryClient();
  const [selectedAlbumID, setSelectedAlbumID] = useState<number | null>(null);
  const [searchText, setSearchText] = useState("");
  const [selectedSearchAlbum, setSelectedSearchAlbum] =
    useState<DiscoveryAlbum | null>(null);
  const [applyingKey, setApplyingKey] = useState<string | null>(null);

  const unmatchedQuery = useQuery({
    queryKey: ["catalog", "unmatched"],
    queryFn: getUnmatchedAlbums,
    enabled: open,
    staleTime: 0,
  });

  const reviewQuery = useQuery({
    queryKey: ["catalog", "unmatched", selectedAlbumID, "review"],
    queryFn: () => getUnmatchedReview(selectedAlbumID as number),
    enabled: open && selectedAlbumID !== null,
    // Review is provider-backed. Keep one album's candidates warm while the
    // admin clicks through the list; the explicit Refresh button covers a
    // deliberate re-probe.
    staleTime: 60_000,
  });

  const searchMutation = useMutation({
    mutationFn: (query: string) => discoverySearch(query, "album"),
  });

  const tracksQuery = useQuery({
    queryKey: [
      "discover",
      "album-tracks",
      selectedSearchAlbum?.provider_name,
      selectedSearchAlbum?.provider_id,
    ],
    queryFn: () =>
      getAlbumTracks(
        selectedSearchAlbum!.provider_id,
        selectedSearchAlbum!.provider_name,
      ),
    enabled: open && selectedSearchAlbum !== null,
  });

  const refreshAfterApply = (response: UnmatchedMatchResponse) => {
    if (response.applied) {
      toast.success(
        response.mode === "release"
          ? `Release match applied (${response.tracks ?? 0} tracks)`
          : "Track match applied",
      );
    } else {
      toast.info("This album is no longer unmatched; the list was refreshed.");
    }
    setSelectedAlbumID(null);
    setSelectedSearchAlbum(null);
    queryClient.invalidateQueries({
      queryKey: ["catalog", "unmatched"],
      exact: true,
    });
    queryClient.invalidateQueries({ queryKey: ["catalog", "status"] });
    queryClient.invalidateQueries({ queryKey: ["library"] });
  };

  const handleMatchError = (prefix: string, error: unknown) => {
    if (error instanceof ApiRequestError && error.status === 409) {
      toast.info("This album was already resolved; refreshing the list.");
      setSelectedAlbumID(null);
      setSelectedSearchAlbum(null);
      queryClient.invalidateQueries({
        queryKey: ["catalog", "unmatched"],
        exact: true,
      });
      queryClient.invalidateQueries({ queryKey: ["catalog", "status"] });
      return;
    }
    toast.error(
      `${prefix}: ${error instanceof Error ? error.message : "Unknown error"}`,
    );
  };

  const applyTrack = useMutation({
    mutationFn: (args: {
      albumID: number;
      trackID: number;
      candidate: UnmatchedReviewCandidate;
    }) =>
      matchUnmatchedAlbum(args.albumID, {
        local_track_id: args.trackID,
        provider: args.candidate.provider,
        provider_track_id: args.candidate.provider_track_id,
      }),
    onSuccess: refreshAfterApply,
    onError: (error) => handleMatchError("Match failed", error),
    onSettled: () => setApplyingKey(null),
  });

  const applyRelease = useMutation({
    mutationFn: (args: {
      albumID: number;
      provider: string;
      providerAlbumID: string;
    }) =>
      matchUnmatchedAlbum(args.albumID, {
        provider: args.provider,
        provider_album_id: args.providerAlbumID,
      }),
    onSuccess: refreshAfterApply,
    onError: (error) => handleMatchError("Release match failed", error),
    onSettled: () => setApplyingKey(null),
  });

  const albums = unmatchedQuery.data ?? [];
  const review = reviewQuery.data;
  const searchAlbums = useMemo(
    () => searchMutation.data?.albums ?? [],
    [searchMutation.data],
  );
  const searchTracks = tracksQuery.data ?? [];
  const applying =
    applyTrack.isPending || applyRelease.isPending;

  const selectAlbum = (album: UnmatchedAlbum) => {
    setSelectedAlbumID(album.album_id);
    setSearchText(`${album.artist} ${album.title}`.trim());
    setSelectedSearchAlbum(null);
    searchMutation.reset();
  };

  const handleOpenChange = (next: boolean) => {
    if (!next) {
      setSelectedAlbumID(null);
      setSearchText("");
      setSelectedSearchAlbum(null);
      setApplyingKey(null);
      searchMutation.reset();
    }
    onOpenChange(next);
  };

  const applyCandidate = (trackID: number, candidate: UnmatchedReviewCandidate) => {
    if (!selectedAlbumID) return;
    setApplyingKey(candidateKey(trackID, candidate));
    applyTrack.mutate({ albumID: selectedAlbumID, trackID, candidate });
  };

  const applySearchTrack = (track: DiscoveryTrack) => {
    if (!selectedAlbumID || !selectedSearchAlbum || !review) return;
    const localTrack = review.tracks[0];
    if (!localTrack) return;
    const candidate = candidateFromSearch(selectedSearchAlbum, track);
    setApplyingKey(`search:${candidateKey(localTrack.track_id, candidate)}`);
    applyTrack.mutate({
      albumID: selectedAlbumID,
      trackID: localTrack.track_id,
      candidate,
    });
  };

  const applySearchRelease = (album: DiscoveryAlbum) => {
    if (!selectedAlbumID) return;
    setApplyingKey(releaseKey(album));
    applyRelease.mutate({
      albumID: selectedAlbumID,
      provider: album.provider_name,
      providerAlbumID: album.provider_id,
    });
  };

  return (
    <Dialog.Root open={open} onOpenChange={handleOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-black/70" />
        <Dialog.Content className="fixed left-1/2 top-1/2 z-50 flex h-[90vh] w-[95vw] max-w-6xl -translate-x-1/2 -translate-y-1/2 flex-col overflow-hidden rounded-lg border border-slate-800 bg-slate-900 shadow-2xl">
          <div className="flex items-center justify-between border-b border-slate-800 px-4 py-3">
            <div>
              <Dialog.Title className="text-base font-semibold text-white">
                Review unmatched albums
              </Dialog.Title>
              <Dialog.Description className="text-xs text-slate-500">
                Play the local file, inspect identity candidates, or search a
                provider. Applying a match writes through the normal catalog
                materialization path.
              </Dialog.Description>
            </div>
            <Dialog.Close
              aria-label="Close"
              className="rounded p-1 text-slate-400 hover:bg-slate-800 hover:text-white"
            >
              <X size={18} />
            </Dialog.Close>
          </div>

          <div className="flex min-h-0 flex-1 flex-col md:flex-row">
            <aside className="w-full shrink-0 overflow-y-auto border-b border-slate-800 p-3 md:w-80 md:border-b-0 md:border-r">
              <p className="mb-2 text-xs text-slate-500">
                {unmatchedQuery.isLoading
                  ? "Loading parked albums…"
                  : `${albums.length} parked album${albums.length === 1 ? "" : "s"}`}
              </p>
              {unmatchedQuery.isError && (
                <p className="text-xs text-red-400">
                  Could not load unmatched albums.
                </p>
              )}
              <div className="space-y-2">
                {albums.map((album) => (
                  <AlbumRow
                    key={album.album_id}
                    album={album}
                    selected={album.album_id === selectedAlbumID}
                    onSelect={() => selectAlbum(album)}
                  />
                ))}
              </div>
              {!unmatchedQuery.isLoading && albums.length === 0 && (
                <p className="text-sm text-slate-500">No unmatched albums.</p>
              )}
            </aside>

            <main className="min-h-0 flex-1 overflow-y-auto p-4">
              {selectedAlbumID === null && (
                <div className="flex h-full items-center justify-center text-center">
                  <p className="max-w-sm text-sm text-slate-500">
                    Select a parked album on the left. A single-track release
                    shows identity candidates; a multi-track album can be
                    matched to a complete provider release.
                  </p>
                </div>
              )}

              {selectedAlbumID !== null && reviewQuery.isLoading && (
                <p className="text-sm text-slate-500">
                  Loading local tracks and candidates…
                </p>
              )}

              {selectedAlbumID !== null && reviewQuery.isError && (
                <p className="text-sm text-red-400">
                  Could not load this album's review. It may have been resolved
                  already; close and reopen the dialog.
                </p>
              )}

              {review && (
                <div className="space-y-4">
                  <div className="flex flex-wrap items-start justify-between gap-3">
                    <div>
                      <h3 className="text-lg font-semibold text-white">
                        {review.artist}
                      </h3>
                      <p className="text-sm text-slate-400">
                        {review.title} · {review.track_count} track
                        {review.track_count === 1 ? "" : "s"}
                      </p>
                    </div>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => reviewQuery.refetch()}
                      loading={reviewQuery.isFetching}
                    >
                      Refresh candidates
                    </Button>
                  </div>

                  {review.track_count > 1 && (
                    <div className="rounded-lg border border-amber-900 bg-amber-950/30 px-3 py-2 text-xs text-amber-300">
                      Multi-track album: choose a complete provider release in
                      the search section below. Per-track merges are not
                      supported.
                    </div>
                  )}

                  {review.track_count === 1 &&
                    review.identity_providers.length === 0 && (
                      <div className="rounded-lg border border-amber-900 bg-amber-950/30 px-3 py-2 text-xs text-amber-300">
                        No identity provider is currently available. Provider
                        search still works for a one-track release.
                      </div>
                    )}

                  <div className="space-y-3">
                    {review.tracks.map((track) => (
                      <LocalTrack
                        key={track.track_id}
                        track={track}
                        applyingKey={applyingKey}
                        disabled={applying}
                        onApply={(candidate) =>
                          applyCandidate(track.track_id, candidate)
                        }
                      />
                    ))}
                  </div>

                  <UnmatchedSearchSection
                    review={review}
                    searchText={searchText}
                    onSearchTextChange={setSearchText}
                    onSearch={(query) => searchMutation.mutate(query)}
                    searching={searchMutation.isPending}
                    searchError={searchMutation.isError}
                    searchSuccess={searchMutation.isSuccess}
                    searchAlbums={searchAlbums}
                    selectedSearchAlbum={selectedSearchAlbum}
                    onSelectAlbum={setSelectedSearchAlbum}
                    tracksLoading={tracksQuery.isLoading}
                    tracksError={tracksQuery.isError}
                    tracksSuccess={tracksQuery.isSuccess}
                    searchTracks={searchTracks}
                    applyingKey={applyingKey}
                    applying={applying}
                    onApplyRelease={applySearchRelease}
                    onApplyTrack={applySearchTrack}
                  />
                </div>
              )}
            </main>
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
