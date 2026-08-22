# Monitoring Service

> Source of truth: `internal/download/monitor_core.go`,
> `internal/download/monitor_dispatch.go`, `internal/download/monitor_retry.go`.

`MonitoringService` drives the download state machine from a single ticker loop.
It replaces the older worker-pool/dispatcher/retry-worker design: one goroutine,
1-second poll interval, each tick wrapped in panic recovery and a 30s watchdog.

## Main Loop

```mermaid
flowchart TD
    A["run(): ticker 1s"] --> B["safeTick()"]
    B --> C{"tick()"}
    C --> D["syncFromProviders()"]
    C --> E["startQueuedDownloads()"]
    C --> F["pollActiveDownloads()"]
    C --> G["checkCancellations()"]
    C --> H{"ticksSinceRetry >= 5?"}
    H -- yes --> I["scanRetry()"]
    H -- no --> J
    I --> J{"ticksSincePendingResolve >= 10?"}
    J -- yes --> K["resolvePendingSources()"]
    J -- no --> B
    K --> B

    B -. "panic or >30s" .-> L["log error, continue next tick"]
```

- **`safeTick()`** runs each tick in a goroutine with a 30s `time.After` watchdog:
  a hung provider HTTP call never blocks the monitor indefinitely.
- **`run()`** starts after `Start()`: orphan recovery + pending-source resolution
  run first, then the poll loop starts. `Shutdown()` cancels the internal context
  and waits for the loop to exit; active downloads are **not** cancelled.

## Tick Phases

```mermaid
flowchart TD
    subgraph sync["syncFromProviders() — orphan reconciliation"]
        A1["For each registered MonitoredProvider"]
        A2["Get ActiveDownloads() (provider-managed IDs)"]
        A1 --> A2
        A2 --> A3{"Provider ID known to us?"}
        A3 -- yes --> A4["keep tracking"]
        A3 -- no --> A5["GetStatus() to match → adopt as groovearr record"]
        A2 --> A6{"Tracked ID missing from ActiveDownloads?"}
        A6 -- yes --> A7["assume provider cleaned up → remove tracking"]
    end

    subgraph start["startQueuedDownloads() — dispatch"]
        B1["List queued records"]
        B1 --> B2{"Already tracked?"}
        B2 -- yes --> B3["skip"]
        B2 -- no --> B4{"IsPendingSource()?"}
        B4 -- yes --> B3
        B4 -- no --> B5{"Non-album with empty Filename?"}
        B5 -- yes --> B3
        B5 -- no --> B6{"RetryAfter not elapsed?"}
        B6 -- yes --> B3
        B6 -- no --> B7{"Record is album?"}
        B7 -- yes --> B8["startAlbumDownload() → DownloadClientRegistry"]
        B7 -- no --> B9["startSingleDownload() → MonitoredProvider"]
    end

    subgraph poll["pollActiveDownloads() — status polling"]
        C1["For each active download"]
        C1 --> C2{"Deadline passed?"}
        C2 -- yes --> C3["failRecord(downloading → failed)"]
        C2 -- no --> C4{"Album download?"}
        C4 -- yes --> C5["pollAlbumDownload() → DownloadClient"]
        C4 -- no --> C6["GetStatus() + GetProgress() via MonitoredProvider"]
        C6 --> C7["UpdateProgress() + fire progress event"]
        C7 --> C8["handleProviderState()"]
    end

    subgraph check["checkCancellations()"]
        D1["Detect records cancelled externally → issue provider Cancel"]
    end
```

## handleProviderState (terminal states from provider)

```mermaid
flowchart TD
    S["provider status.State"] --> P
    P{"State == Imported / ImportPending?"}
    P -- yes --> T1["TransitionState: downloading → importPending"]
    T1 --> T2["UpdateProgress(100, filePath, coverURL)"]
    T2 --> T3["Publish TopicDownloadCompleted"]
    T3 --> T4["removeTracking + releaseSemaphore"]
    P -- no --> F{"State == Failed?"}
    F -- yes --> F1["failRecord(downloading → failed)"]
    F1 --> F2["removeTracking + releaseSemaphore"]
    F -- no --> I{"State == Ignored?"}
    I -- yes --> I1["removeTracking + releaseSemaphore (no record state change)"]
    I -- no --> D["still in progress — poll again next tick"]
```

## Retry & Pending Resolution (every N ticks)

```mermaid
flowchart TD
    subgraph retry["scanRetry() — every 5 ticks"]
        R1["List failed + failedPending"]
        R1 --> R2{"RetryCount >= MaxRetries?"}
        R2 -- yes --> R3["skip (terminal)"]
        R2 -- no --> R4{"RetryAfter not elapsed?"}
        R4 -- yes --> R3
        R4 -- no --> R5["resolveRetrySource(): FindBestMatch across providers"]
        R5 --> R6{"Source found?"}
        R6 -- no --> R3
        R6 -- yes --> R7["RetryCount++, backoff = jitterBackoff(2^count, 60)"]
        R7 --> R8["state → queued, clear error/progress, persist"]
        R8 --> R9["Publish stateChanged"]
    end

    subgraph pending["resolvePendingSources() — every 10 ticks + at startup"]
        P1["List queued records with SourceName == 'pending'"]
        P1 --> P2["FindBestMatch (artist, title)"]
        P2 --> P3{"Found?"}
        P3 -- yes --> P4["set SourceName/Filename/size/bitrate, stay queued"]
        P3 -- no --> P5["state → failedPending, RetryCount=1, RetryAfter=1min"]
    end
```

## Key Facts

- **`syncFromProviders()`** (undocumented in the old ASCII docs): reconciles
  the provider's `ActiveDownloads()` against our tracking map every tick —
  adopts unknown provider IDs by matching `GetStatus()`, drops tracking for IDs
  the provider no longer reports.
- **Concurrency** — per-plugin/per-client semaphores (buffered channels) gate
  dispatch. A full pool leaves records `queued` for the next tick.
- **Timeouts** — per-download deadline = `now + DownloadTimeout()`; enforced in
  `pollSingle`/`pollAlbumDownload` before status polling.
