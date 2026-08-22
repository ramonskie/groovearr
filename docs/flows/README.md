# Flow Charts

Code-verified mermaid flow charts for Groovearr's core components. Each chart
was verified against the working-tree source files listed at the top of the
document (files are the source of truth; the old ASCII diagrams in
`docs/architecture.md` contain drift noted inline).

| Chart | Covers | Key files |
|---|---|---|
| [Download State Machine](download-state-machine.md) | 8-state lifecycle: queued / downloading / importPending / importing / imported / failedPending / failed / ignored | `internal/download/types.go`, `service.go`, `monitor_dispatch.go`, `monitor_retry.go`, `importer.go` |
| [Monitoring Service](monitoring-service.md) | 1s tick loop, orphan reconciliation, dispatch (track vs album), status polling, retry + pending-source resolution | `internal/download/monitor_core.go`, `monitor_dispatch.go`, `monitor_retry.go` |
| [Import Handler Chain](import-handler-chain.md) | 7-handler post-download chain, album vs track routing, terminal-state re-checks | `internal/download/importer.go`, `handler_*.go`, `cmd/groovearr/app.go` |
| [Album Import Handler](album-import-handler.md) | Post-download folder scan → track resolution → file matching → synthetic records → discovery cache | `internal/download/handler_album_import.go` |
| [Metadata Enrichment](metadata-enrichment.md) | Provider loop, album/cover/track enrichment, cooldown handling, bulk vs per-download | `internal/download/handler_enrichment.go`, `internal/metadata/` |
| [Queue-Time Resolution](queue-time-resolution.md) | Pre-queue album/cover lookup via `MetadataResolver.EnrichMetadata` (best-effort, no cooldown gating) | `internal/metadata/resolver.go` |

## Known Drift (old ASCII docs vs. current code)

1. `failedPending` state exists — absent from `architecture.md`.
2. `Cancel()` transitions to `ignored` (docs said `failed`).
3. Album dispatch uses `DownloadClient.AddDownload(uri, "music", savePath)`
   (docs said `Add(url)`).
4. `syncFromProviders()` orphan reconciliation — undocumented.
5. Provider-side `ignored` handling — undocumented.
6. Pending-source flow (`QueuePending` → `resolvePendingSources` →
   `failedPending`) — undocumented.
7. Import chain re-checks terminal state between handlers — undocumented.
