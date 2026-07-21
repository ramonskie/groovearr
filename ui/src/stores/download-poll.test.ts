import { describe, it, expect } from "vitest";
import { useDownloadStore } from "./download-poll";
import type { DownloadRecord } from "../api/types";

// Helper to build a full download record (simulating HTTP fetch response).
function fullRecord(overrides: Partial<DownloadRecord> = {}): DownloadRecord {
  return {
    id: "soulseek-123",
    source_name: "soulseek",
    display_name: "Daft Punk - Get Lucky",
    filename: "Daft Punk - Get Lucky.flac",
    state: "downloading",
    progress: 0,
    size: 10000000,
    transferred: 0,
    speed: 0,
    file_path: "",
    error: "",
    track_id: "",
    cover_url: "",
    playlist_id: "",
    artist: "Daft Punk",
    album: "Random Access Memories",
    title: "Get Lucky",
    track_number: 7,
    disc_number: 1,
    year: 2013,
    library_track_id: 0,
    ...overrides,
  };
}

// Helper to build a skeleton progress event (simulating SSE fireProgress).
function progressSkeleton(overrides: Partial<DownloadRecord> = {}): DownloadRecord {
  return {
    id: "soulseek-123",
    source_name: "",
    display_name: "",
    filename: "",
    state: "downloading" as const,
    progress: 42.5,
    size: 10000000,
    transferred: 4250000,
    speed: 500000,
    file_path: "",
    error: "",
    track_id: "",
    cover_url: "",
    playlist_id: "",
    artist: "",
    album: "",
    title: "",
    track_number: 0,
    disc_number: 0,
    year: 0,
    library_track_id: 0,
    ...overrides,
  };
}

describe("download-poll store", () => {
  it("preserves metadata when SSE progress arrives", () => {
    const store = useDownloadStore.getState();

    // Initial full load (HTTP GET /api/downloads).
    store.setRecords([fullRecord()]);

    // Simulate SSE fireProgress event — only progress fields populated.
    store.upsertRecord(progressSkeleton());

    const record = useDownloadStore.getState().records["soulseek-123"];
    expect(record.display_name).toBe("Daft Punk - Get Lucky");
    expect(record.source_name).toBe("soulseek");
    expect(record.artist).toBe("Daft Punk");
    expect(record.title).toBe("Get Lucky");
    expect(record.progress).toBe(42.5);
    expect(record.transferred).toBe(4250000);
  });

  it("preserves metadata when SSE state-changed arrives", () => {
    const store = useDownloadStore.getState();

    store.setRecords([fullRecord()]);

    // Simulate SSE publishRecord — only id + state populated.
    store.upsertRecord({
      id: "soulseek-123",
      state: "imported",
      source_name: "",
      display_name: "",
      filename: "",
      progress: 0,
      size: 0,
      transferred: 0,
      speed: 0,
      file_path: "",
      error: "",
      track_id: "",
      cover_url: "",
      playlist_id: "",
      artist: "",
      album: "",
      title: "",
      track_number: 0,
      disc_number: 0,
      year: 0,
      library_track_id: 0,
    });

    const record = useDownloadStore.getState().records["soulseek-123"];
    expect(record.display_name).toBe("Daft Punk - Get Lucky");
    expect(record.source_name).toBe("soulseek");
    expect(record.state).toBe("imported");
  });
});
