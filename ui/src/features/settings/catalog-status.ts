import type { CatalogStatus } from "../../api/types";

export interface CatalogStatusView {
  enabledLabel: string;
  queueLabel: string;
  albumLabel: string;
  artistLabel: string;
  engineLabel: string;
  lastDrainLabel: string;
}

/** Compact relative time for a past ISO timestamp, e.g. "5m ago". */
export function formatRelative(iso: string, now: Date): string {
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return "unknown";
  const seconds = Math.max(0, Math.round((now.getTime() - then) / 1000));
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.round(hours / 24)}d ago`;
}

/**
 * Pure projection of a CatalogStatus into the labels the status card renders.
 * Kept out of the component so the formatting is testable in isolation.
 */
export function describeCatalogStatus(
  status: CatalogStatus,
  now: Date,
): CatalogStatusView {
  const { queue, albums, artists, engine } = status;

  const queueParts = [`${queue.queued} queued`, `${queue.due_now} due now`];
  if (queue.scheduled > 0) queueParts.push(`${queue.scheduled} scheduled`);

  const albumParts = [`${albums.fresh} fresh`, `${albums.stale} stale`];
  if (albums.pending > 0) albumParts.push(`${albums.pending} pending`);
  albumParts.push(`${albums.unmatched} unmatched`, `${albums.failed} failed`);

  const lastDrainLabel = engine.last_drain_at
    ? `${engine.last_drain_count} item${engine.last_drain_count === 1 ? "" : "s"} · ${formatRelative(engine.last_drain_at, now)}`
    : "no drain yet";

  // A zero interval/batch means no engine was wired (bare/defensive server), so
  // report "unavailable" rather than a misleading "every 0s · batch 0".
  const engineWired = engine.interval_seconds > 0 || engine.batch_size > 0;
  const engineLabel = engineWired
    ? `${engine.running ? "running" : "idle"} · every ${engine.interval_seconds}s · batch ${engine.batch_size}`
    : "unavailable";

  return {
    enabledLabel: status.enabled ? "enabled" : "disabled",
    queueLabel: queueParts.join(" · "),
    albumLabel: albumParts.join(" · "),
    artistLabel: `${artists.materialized} materialized`,
    engineLabel,
    lastDrainLabel,
  };
}
