import { libraryTrackStreamUrl } from "../../api/client";
import type {
  UnmatchedAlbum,
  UnmatchedReviewCandidate,
  UnmatchedReviewTrack,
} from "../../api/types";
import Button from "../../components/Button";
import {
  candidateKey,
  formatDuration,
  providerLabel,
} from "./unmatched-review-helpers";

function evidence(c: UnmatchedReviewCandidate): string[] {
  const out: string[] = [];
  if (c.signals.isrc_exact) out.push("ISRC");
  if (c.signals.provider_track_id_exact) out.push("track ID");
  if (c.signals.title_matches) out.push("title");
  if (c.signals.artist_matches) out.push("artist");
  if (c.signals.duration_within_tolerance) out.push("duration");
  if (c.signals.album_title_matches) out.push("album");
  if (c.signals.track_count_matches) out.push("track count");
  if (c.signals.track_number_matches) out.push("track number");
  return out;
}

export function AlbumRow({
  album,
  selected,
  onSelect,
}: {
  album: UnmatchedAlbum;
  selected: boolean;
  onSelect: () => void;
}) {
  return (
    <button
      type="button"
      aria-pressed={selected}
      onClick={onSelect}
      className={`w-full rounded-lg border px-3 py-2 text-left transition-colors ${
        selected
          ? "border-purple-500 bg-purple-950/30"
          : "border-slate-800 bg-slate-900 hover:border-slate-700"
      }`}
    >
      <p className="truncate text-sm font-medium text-white">{album.title}</p>
      <p className="truncate text-xs text-slate-400">{album.artist}</p>
    </button>
  );
}

export function CandidateCard({
  trackID,
  candidate,
  applying,
  disabled,
  onApply,
}: {
  trackID: number;
  candidate: UnmatchedReviewCandidate;
  applying: boolean;
  disabled: boolean;
  onApply: () => void;
}) {
  const conflict =
    candidate.signals.duration_known &&
    !candidate.signals.duration_within_tolerance;
  return (
    <div className="rounded-lg border border-slate-700 bg-slate-800/60 p-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="rounded bg-slate-700 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-slate-200">
          {providerLabel(candidate.provider)}
        </span>
        {candidate.auto && (
          <span className="rounded bg-emerald-900/60 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-emerald-300">
            auto
          </span>
        )}
        <span className="ml-auto text-xs font-medium text-slate-300">
          score {candidate.score}
        </span>
      </div>
      <p className="mt-2 text-sm font-medium text-white">{candidate.title}</p>
      <p className="text-xs text-slate-400">
        {candidate.artist} · {candidate.album}
      </p>
      <p className="mt-1 text-xs text-slate-500">
        {formatDuration(candidate.duration_ms)}
        {candidate.isrc ? ` · ${candidate.isrc}` : ""}
        {candidate.album_track_count
          ? ` · ${candidate.album_track_count}-track release`
          : ""}
      </p>

      {conflict && (
        <p className="mt-2 rounded bg-red-950/50 px-2 py-1 text-xs text-red-300">
          Known duration conflict — the local file and this candidate differ by
          more than the matching tolerance.
        </p>
      )}

      <div className="mt-2 flex flex-wrap gap-1">
        {evidence(candidate).map((item) => (
          <span
            key={item}
            className="rounded bg-slate-900 px-1.5 py-0.5 text-[10px] text-slate-400"
          >
            {item}
          </span>
        ))}
      </div>

      <Button
        size="sm"
        className="mt-3"
        loading={applying}
        disabled={disabled}
        onClick={onApply}
      >
        Use this match
      </Button>
      <span className="ml-2 text-[10px] text-slate-500">
        local track {trackID}
      </span>
    </div>
  );
}

export function LocalTrack({
  track,
  applyingKey,
  disabled,
  onApply,
}: {
  track: UnmatchedReviewTrack;
  applyingKey: string | null;
  disabled: boolean;
  onApply: (candidate: UnmatchedReviewCandidate) => void;
}) {
  return (
    <div className="rounded-lg border border-slate-800 bg-slate-900/60 p-3">
      <div className="flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
        <div className="min-w-0">
          <p className="text-sm font-medium text-white">
            {track.track_number ? `${track.track_number}. ` : ""}
            {track.title}
          </p>
          <p className="text-xs text-slate-500">
            {formatDuration(track.duration_ms)}
            {track.isrc ? ` · ${track.isrc}` : ""}
          </p>
        </div>
        <audio
          controls
          preload="none"
          aria-label={`Play ${track.title}`}
          src={libraryTrackStreamUrl(track.track_id)}
          className="h-8 w-full max-w-xs"
        />
      </div>

      {track.candidates.length > 0 && (
        <div className="mt-3 grid gap-2 lg:grid-cols-2">
          {track.candidates.map((candidate) => (
            <CandidateCard
              key={candidateKey(track.track_id, candidate)}
              trackID={track.track_id}
              candidate={candidate}
              applying={applyingKey === candidateKey(track.track_id, candidate)}
              disabled={disabled}
              onApply={() => onApply(candidate)}
            />
          ))}
        </div>
      )}
    </div>
  );
}
