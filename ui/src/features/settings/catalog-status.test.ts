import { describe, expect, it } from "vitest";
import type { CatalogStatus } from "../../api/types";
import { describeCatalogStatus, formatRelative } from "./catalog-status";

const now = new Date("2026-10-12T12:00:00Z");

function status(overrides: Partial<CatalogStatus> = {}): CatalogStatus {
  return {
    enabled: true,
    queue: { queued: 0, due_now: 0, scheduled: 0 },
    albums: { pending: 0, fresh: 0, stale: 0, unmatched: 0, failed: 0 },
    artists: { materialized: 0 },
    engine: {
      running: false,
      interval_seconds: 60,
      batch_size: 8,
      last_drain_at: null,
      last_drain_count: 0,
    },
    ...overrides,
  };
}

describe("formatRelative", () => {
  it("formats seconds, minutes, hours and days", () => {
    expect(formatRelative("2026-10-12T11:59:30Z", now)).toBe("30s ago");
    expect(formatRelative("2026-10-12T11:55:00Z", now)).toBe("5m ago");
    expect(formatRelative("2026-10-12T09:00:00Z", now)).toBe("3h ago");
    expect(formatRelative("2026-10-10T12:00:00Z", now)).toBe("2d ago");
  });

  it("returns 'unknown' for an unparseable timestamp", () => {
    expect(formatRelative("not-a-date", now)).toBe("unknown");
  });

  it("clamps a future timestamp to zero seconds", () => {
    expect(formatRelative("2026-10-12T12:00:30Z", now)).toBe("0s ago");
  });
});

describe("describeCatalogStatus", () => {
  it("reports an idle, disabled catalog with no drain", () => {
    const view = describeCatalogStatus(status({ enabled: false }), now);
    expect(view.enabledLabel).toBe("disabled");
    expect(view.queueLabel).toBe("0 queued · 0 due now");
    expect(view.albumLabel).toBe("0 fresh · 0 stale · 0 unmatched · 0 failed");
    expect(view.artistLabel).toBe("0 materialized");
    expect(view.engineLabel).toBe("idle · every 60s · batch 8");
    expect(view.lastDrainLabel).toBe("no drain yet");
  });

  it("includes scheduled and pending only when non-zero", () => {
    const view = describeCatalogStatus(
      status({
        queue: { queued: 9, due_now: 2, scheduled: 7 },
        albums: { pending: 3, fresh: 10, stale: 1, unmatched: 2, failed: 4 },
      }),
      now,
    );
    expect(view.queueLabel).toBe("9 queued · 2 due now · 7 scheduled");
    expect(view.albumLabel).toBe(
      "10 fresh · 1 stale · 3 pending · 2 unmatched · 4 failed",
    );
  });

  it("formats a running engine with a last-drain summary", () => {
    const view = describeCatalogStatus(
      status({
        artists: { materialized: 12 },
        engine: {
          running: true,
          interval_seconds: 30,
          batch_size: 4,
          last_drain_at: "2026-10-12T11:59:00Z",
          last_drain_count: 1,
        },
      }),
      now,
    );
    expect(view.engineLabel).toBe("running · every 30s · batch 4");
    expect(view.artistLabel).toBe("12 materialized");
    expect(view.lastDrainLabel).toBe("1 item · 1m ago");
  });

  it("pluralizes the drain count", () => {
    const view = describeCatalogStatus(
      status({
        engine: {
          running: true,
          interval_seconds: 60,
          batch_size: 8,
          last_drain_at: "2026-10-12T11:50:00Z",
          last_drain_count: 3,
        },
      }),
      now,
    );
    expect(view.lastDrainLabel).toBe("3 items · 10m ago");
  });

  it("reports an unwired engine as unavailable", () => {
    const view = describeCatalogStatus(
      status({
        engine: {
          running: false,
          interval_seconds: 0,
          batch_size: 0,
          last_drain_at: null,
          last_drain_count: 0,
        },
      }),
      now,
    );
    expect(view.engineLabel).toBe("unavailable");
  });
});
