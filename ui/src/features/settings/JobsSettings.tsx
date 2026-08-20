import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  useJobState,
  useStartScanJob,
  useStartEnrichJob,
  useStartOrganizeJob,
  useStartDuplicatesJob,
  useCancelJob,
} from "../../hooks/use-job";
import {
  getOrganizeReport,
  getArtistDuplicates,
  mergeArtists,
} from "../../api/client";
import Card from "../../components/Card";
import Button from "../../components/Button";

export default function JobsSettings() {
  const jobQuery = useJobState();
  const startScan = useStartScanJob();
  const startEnrich = useStartEnrichJob();
  const startOrganize = useStartOrganizeJob();
  const startDuplicates = useStartDuplicatesJob();
  const cancel = useCancelJob();

  const reportQuery = useQuery({
    queryKey: ["organize", "report"],
    queryFn: getOrganizeReport,
  });
  const queryClient = useQueryClient();
  const merge = useMutation({
    mutationFn: (ids: { keep: number; remove: number }) =>
      mergeArtists(ids.keep, ids.remove),
    onSuccess: (res) => {
      const moving = res.organize_started
        ? "Moving files…"
        : "Run Organize · Repair to move the files";
      if (res.renamed && res.canonical_name) {
        toast.success(
          `Artists merged — renamed to "${res.canonical_name}". ${moving}`,
        );
      } else {
        toast.success(`Artists merged — ${moving}`);
      }
      duplicatesQuery.refetch();
      queryClient.invalidateQueries({ queryKey: ["library"] });
    },
    onError: (err) => {
      toast.error(
        `Merge failed: ${err instanceof Error ? err.message : "Unknown error"}`,
      );
    },
  });

  // The duplicates list is only fetched after a duplicate check has run; it
  // reads the persisted scan, so no provider lookups happen on page load.
  const [duplicatesEnabled, setDuplicatesEnabled] = useState(false);
  const duplicatesQuery = useQuery({
    queryKey: ["artists", "duplicates"],
    queryFn: getArtistDuplicates,
    enabled: duplicatesEnabled,
  });

  const job = jobQuery.data;
  const running = job?.state === "running";
  const pct = Math.round(job?.progress ?? 0);
  const report = reportQuery.data;
  const groups = duplicatesQuery.data?.groups ?? [];

  // Reveal the duplicates list once a duplicate check completes. Fires for
  // fast completions (job finishes inside the POST round-trip, so no
  // Reveal the duplicates list whenever a completed duplicate check is
  // observed — covering slow jobs (running→completed), fast jobs that finish
  // inside the POST round-trip (completed with no prior running observation),
  // and a check that completed before this page loaded. Idempotent.
  useEffect(() => {
    if (job?.type === "duplicates" && job.state === "completed") {
      setDuplicatesEnabled(true);
    }
  }, [job]);

  // Per-group merge target, defaulting to the suggested (first) entry.
  const [targets, setTargets] = useState<Record<string, number>>({});
  useEffect(() => {
    setTargets((prev) => {
      const next = { ...prev };
      for (const g of groups) {
        if (next[g.name] == null && g.artists.length > 0) {
          next[g.name] = g.artists[0].id;
        }
      }
      return next;
    });
  }, [groups]);

  const runOrganize = (dryRun: boolean) => {
    if (!dryRun && !window.confirm("Run organize repair? This moves files on disk.")) {
      return;
    }
    startOrganize.mutate(dryRun);
  };

  const jobTitle =
    job?.type === "organize"
      ? "Organize"
      : job?.type === "scan"
        ? "Scan"
        : job?.type === "duplicates"
          ? "Duplicate Check"
          : "Enrichment";

  return (
    <div className="space-y-4">
      <Card title="Background Jobs">
        <p className="text-sm text-slate-400">
          Run long-running library tasks in the background. Scan imports new or
          changed audio files into the library; Enrich fills in missing
          metadata (ISRC, genres, release dates, artwork) from the configured
          metadata providers; Organize moves files into the configured folder
          template layout.
        </p>

        <div className="mt-4 flex flex-wrap gap-2">
          <Button
            variant="primary"
            onClick={() => startScan.mutate()}
            disabled={running}
          >
            Scan Library
          </Button>
          <Button
            variant="success"
            onClick={() => startEnrich.mutate()}
            disabled={running}
          >
            Enrich Library
          </Button>
          <Button
            variant="primary"
            onClick={() => runOrganize(true)}
            disabled={running}
          >
            Organize · Dry Run
          </Button>
          <Button
            variant="danger"
            onClick={() => runOrganize(false)}
            disabled={running}
          >
            Organize · Repair
          </Button>
        </div>
      </Card>

      {job && (
        <Card title={jobTitle}>
          <div className="flex items-center justify-between text-sm">
            <span className="flex items-center gap-2">
              <span className="capitalize text-slate-300">{job.state}</span>
              <span className="font-medium text-white">{pct}%</span>
            </span>
            {running && (
              <Button
                variant="danger"
                size="sm"
                onClick={() => cancel.mutate()}
              >
                Cancel
              </Button>
            )}
          </div>

          <div className="mt-2 h-2 w-full overflow-hidden rounded-full bg-slate-800">
            <div
              className="h-full rounded-full bg-purple-500 transition-all duration-300"
              style={{ width: `${pct}%` }}
            />
          </div>

          <div className="mt-2 text-xs text-slate-400">
            {job.total > 0 && (
              <span>
                {job.done} / {job.total}
                {" · "}
              </span>
            )}
            <span className="truncate" title={job.message}>
              {job.message || "Idle"}
            </span>
          </div>

          {job.state === "failed" && job.error && (
            <div className="mt-2 text-xs text-red-400">{job.error}</div>
          )}
        </Card>
      )}

      <Card title="Organize Report">
        <p className="text-sm text-slate-400">
          Results of the last organize job. Dry run lists which tracks would
          move and where; repair lists what was actually moved.
        </p>

        {report ? (
          <div className="mt-3">
            <div className="flex flex-wrap gap-x-4 gap-y-1 text-sm">
              <span className="capitalize text-slate-300">{report.mode}</span>
              <span className="text-slate-400">
                {new Date(report.ran_at).toLocaleString()}
              </span>
              <span className="text-emerald-400">
                {report.mode === "dry run" ? "would move" : "moved"}:{" "}
                {report.mode === "dry run"
                  ? report.summary.would_move
                  : report.summary.moved}
              </span>
              <span className="text-slate-400">
                in place: {report.summary.in_place}
              </span>
              <span className="text-amber-400">
                skipped: {report.summary.skipped}
              </span>
              <span className="text-red-400">
                errors: {report.summary.errors}
              </span>
            </div>

            {report.entries.length > 0 && (
              <ul className="mt-3 max-h-72 divide-y divide-slate-800 overflow-y-auto rounded-lg border border-slate-800 text-xs">
                {report.entries.slice(0, 300).map((e, i) => (
                  <li key={i} className="flex items-start gap-2 px-3 py-1.5">
                    <span
                      className={
                        e.reason === "errors" || e.reason === "skipped"
                          ? "shrink-0 text-amber-400"
                          : "shrink-0 text-emerald-400"
                      }
                    >
                      {e.reason}
                    </span>
                    <span className="min-w-0 break-all text-slate-300">
                      {e.from}
                      <span className="text-slate-500"> → </span>
                      {e.to}
                    </span>
                  </li>
                ))}
              </ul>
            )}
            {report.entries.length === 0 && (
              <p className="mt-3 text-sm text-slate-500">
                No entries — every track was in place (or the job errored).
              </p>
            )}
            {report.truncated && (
              <p className="mt-2 text-xs text-slate-500">
                List truncated; summary shows full counts.
              </p>
            )}
          </div>
        ) : (
          <p className="mt-3 text-sm text-slate-500">
            No organize job has run yet. Start a dry run to get a health report.
          </p>
        )}
      </Card>

      <Card title="Artist Duplicates">
        <p className="text-sm text-slate-400">
          Artists whose names differ only by case (e.g. &quot;Acda en de
          Munnik&quot; vs &quot;Acda en De Munnik&quot;) are stored as
          separate artists. Run a check to scan for them and resolve the
          canonical provider spelling. Click a name to pick which artist keeps
          its identity — the suggested one is pre-selected.
        </p>

        {!duplicatesEnabled ? (
          <div className="mt-3">
            <Button
              variant="primary"
              onClick={() => startDuplicates.mutate()}
              disabled={running || startDuplicates.isPending}
            >
              Check for duplicates
            </Button>
          </div>
        ) : groups.length === 0 ? (
          <p className="mt-3 text-sm text-slate-500">
            No case-insensitive duplicates found.
          </p>
        ) : (
          <div className="mt-3 space-y-2">
            {groups.map((g) => {
              const keepId = targets[g.name] ?? g.artists[0]?.id;
              const targetName =
                g.artists.find((a) => a.id === keepId)?.name ??
                g.artists[0]?.name ??
                "";
              return (
                <div
                  key={g.name}
                  className="rounded-lg border border-slate-800 p-3"
                >
                  <p className="mb-1 text-xs font-medium text-slate-400">
                    {g.artists.length} artists match &quot;{g.name}&quot;
                  </p>
                  <div className="space-y-1">
                    {g.artists.map((a) => {
                      const selected = keepId === a.id;
                      const suggested = a.id === g.artists[0].id;
                      return (
                        <div
                          key={a.id}
                          onClick={() =>
                            setTargets((prev) => ({ ...prev, [g.name]: a.id }))
                          }
                          className={`flex cursor-pointer items-center justify-between gap-2 rounded border px-2 py-1.5 text-sm transition-colors ${
                            selected
                              ? "border-emerald-500/60 bg-emerald-500/10"
                              : "border-transparent hover:border-slate-700"
                          }`}
                        >
                          <span className="min-w-0 truncate text-slate-200">
                            {a.name}{" "}
                            <span className="text-xs text-slate-500">
                              ({a.track_count} tracks)
                            </span>
                            {selected && (
                              <span className="ml-1 text-xs text-emerald-400">
                                {suggested ? "suggestion" : "selected"}
                              </span>
                            )}
                          </span>
                          {!selected && (
                            <Button
                              variant="primary"
                              disabled={merge.isPending}
                              onClick={(e) => {
                                e.stopPropagation();
                                merge.mutate({ keep: keepId, remove: a.id });
                              }}
                            >
                              Merge into {targetName}
                            </Button>
                          )}
                        </div>
                      );
                    })}
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </Card>
    </div>
  );
}
