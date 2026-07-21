import { create } from "zustand";
import type { DownloadRecord, DownloadState } from "../api/types";

// ─── SSE connection state ───────────────────────────────────────────

type SseStatus = "disconnected" | "connecting" | "connected";

// ─── Store shape ────────────────────────────────────────────────────

interface DownloadStoreState {
  /** Map of download ID → live record (populated by SSE or polling). */
  records: Record<string, DownloadRecord>;
  /** Whether the download list is actively being polled (fallback). */
  pollingActive: boolean;
  /** Number of downloads currently in progress (non-terminal). */
  activeCount: number;
  /** Current SSE connection status. */
  sseStatus: SseStatus;

  // ── Polling controls ─────────────────────────────────────────────
  startPolling: () => void;
  stopPolling: () => void;
  setActiveCount: (count: number) => void;

  // ── Event-driven record updates (SSE path) ───────────────────────
  /** Replace all records (initial load or polling refresh). */
  setRecords: (records: DownloadRecord[]) => void;
  /** Upsert a single record from an SSE event. */
  upsertRecord: (record: DownloadRecord) => void;
  /** Remove a record (e.g. cancelled/cleared). */
  removeRecord: (id: string) => void;

  // ── SSE status ───────────────────────────────────────────────────
  setSseStatus: (status: SseStatus) => void;
}

// ─── Helpers ────────────────────────────────────────────────────────

const TERMINAL_STATES: Set<DownloadState> = new Set([
  "imported",
  "failed",
  "ignored",
]);

function countActive(records: DownloadRecord[]): number {
  return records.filter((r) => !TERMINAL_STATES.has(r.state)).length;
}

// ─── Store ──────────────────────────────────────────────────────────

export const useDownloadStore = create<DownloadStoreState>()((set) => ({
  records: {},
  pollingActive: false,
  activeCount: 0,
  sseStatus: "disconnected",

  startPolling: () => set({ pollingActive: true }),
  stopPolling: () => set({ pollingActive: false }),
  setActiveCount: (count) => set({ activeCount: count }),

  setRecords: (records) =>
    set((state) => {
      const map: Record<string, DownloadRecord> = {};
      for (const r of records) {
        map[r.id] = r;
      }
      return {
        records: map,
        activeCount: countActive(records),
      };
    }),

  upsertRecord: (record) =>
    set((state) => {
      const existing = state.records[record.id];
      // Merge: SSE events carry only progress/state fields. Preserve
      // existing metadata (display_name, source_name, artist, title, etc.)
      // by having existing take priority, then explicitly applying the
      // mutable fields that SSE events are intended to update.
      const merged = existing
        ? {
            ...existing,
            state: record.state || existing.state,
            progress: record.progress,
            size: record.size || existing.size,
            transferred: record.transferred,
            speed: record.speed,
            file_path: record.file_path || existing.file_path,
            error: record.error || existing.error,
          }
        : record;
      const next = { ...state.records, [record.id]: merged };
      const arr = Object.values(next);
      return {
        records: next,
        activeCount: countActive(arr),
      };
    }),

  removeRecord: (id) =>
    set((state) => {
      const next = { ...state.records };
      delete next[id];
      const arr = Object.values(next);
      return {
        records: next,
        activeCount: countActive(arr),
      };
    }),

  setSseStatus: (status) => set({ sseStatus: status }),
}));
