# Album Import Handler

> Source of truth: `internal/download/handler_album_import.go`.

`AlbumImportHandler` processes completed **album** downloads (torrents via
Prowlarr → qBittorrent, or any provider that reports a folder). It is invoked by
`CompletedDownloadService.onDownloadCompleted` when `record.IsAlbum()` is true.

## Flowchart

```mermaid
flowchart TD
    START["AlbumImportHandler.Handle(record)"] --> CHK{"record.IsAlbum()?"}
    CHK -- no --> ERR1["error: not an album download"]

    CHK -- yes --> SCAN["scanAudioFiles(folderPath)<br/>recursive audio file discovery"]
    SCAN --> SCANK{"no audio files?"}
    SCANK -- yes --> ERR2["error: no audio files found"]
    SCANK -- no --> RESOLVE["trackResolver(sourceName, artist, album,<br/>fileCount, torrentTitle)"]

    RESOLVE --> RESOLVEK{"resolver configured<br/>and returned tracks?"}
    RESOLVEK -- error --> FB["fallback: use record.AlbumTracks"]
    RESOLVEK -- yes --> SETMB["set record.AlbumMBID<br/>(MusicBrainz release MBID)"]
    SETMB --> MATCH
    FB --> MATCH

    MATCH["matchFiles(audioFiles, tracks)<br/>track-number + filename matching"] --> LOOP

    subgraph LOOP["for each matched file"]
        M1["create synthetic Record<br/>ID = '{parentID}-t{n:02d}'"]
        M2["dlStore.Insert(synth)"]
        M3["run importChain sequentially<br/>(same 7 handlers as track pipe)"]
        M4["chain error? → log warn, continue"]
        M5["dlStore.Delete(synth)<br/>synthetic record is transient"]
        M6["collect synth.LibraryTrackID"]
        M1 --> M2 --> M3 --> M4 --> M5 --> M6
    end

    LOOP --> SYNC["record.ImportedTrackIDs = importedIDs<br/>dlStore.Update(parent)"]
    SYNC --> CACHE["updateDiscoveryCache(trackIDs)<br/>rebuild album_discovery_cache with<br/>actual library tracks (provider='library')"]
    CACHE --> DONE["done"]
```

## Step Details (code-verified)

1. **`scanAudioFiles`** — recursively finds audio files in the downloaded
   folder (`record.FilePath` or `record.FolderPath`).
2. **`trackResolver`** — a function type wired in `cmd/groovearr/app.go`
   (`newTrackResolver`) bridging to Prowlarr's `ResolveTracksForCount`:
   `MusicBrainz.SearchReleasesByGroup(rgid)` → one API call returns all releases
   in the release group with track counts → `pickBestMatchingRelease` picks the
   release whose track count is closest to the file count (word-overlap
   tiebreaker with the torrent title, e.g. "Ten Redux" vs "Ten").
3. **`matchFiles`** — pairs filesystem files with expected tracks by track
   number + cleaned filename.
4. **Synthetic records** — each matched file becomes a transient `Record` with
   `ID = "{parentID}-t{index:02d}"`, `State = StateImporting`, and runs through
   the **same** 7-handler `importChain` as single-track downloads (see
   [import-handler-chain.md](import-handler-chain.md)). The chain sets
   `LibraryTrackID` during `LibraryImporterHandler.Handle()`.
5. **Cleanup** — synthetic records are deleted from the downloads store after
   the chain; they never appear in the downloads list.
6. **`updateDiscoveryCache`** — writes the actual library tracks under
   `provider_name = 'library'`, overriding stale discovery-provider data
   (e.g. 17-track "Ten Redux" instead of Deezer's 11-track standard edition).

## Key Facts

- Album import is **post-download** — track resolution happens only after the
  torrent completes, using the **actual file count** on disk.
- The parent record's `AlbumMBID` flows into the import chain via each
  synthetic record, enabling accurate cover art lookups (Cover Art Archive) and
  MBID sync to the library album.
- Synthetic per-track failures are logged but do not fail the parent album —
  the chain error on a single track is non-fatal; `importedIDs` collects
  whatever succeeded.
