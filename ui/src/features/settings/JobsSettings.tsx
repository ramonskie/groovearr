import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  useJobState,
  useStartScanJob,
  useStartEnrichJob,
  useStartOrganizeJob,
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
  const cancel = useCancelJob();

  const reportQuery = useQuery({
    queryKey: ["organize", "report"],
    queryFn: getOrganizeReport,
  });
  const duplicatesQuery = useQuery({
    queryKey: ["artists", "duplicates"],
    queryFn: getArtistDuplicates,
  });
  const queryClient = useQueryClient();
  const merge = useMutation({
    mutationFn: (ids: { keep: number; remove: number }) =>
      mergeArtists(ids.keep, ids.remove),
    onSuccess: (res) => {
      if (res.renamed && res.canonical_name) {
        toast.success(
          `Artists merged — renamed to "${res.canonical_name}". Run Organize · Repair to move the files`,
        );
      } else {
        toast.success(
          "Artists merged — run Organize · Repair to move the files",
        );
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

  const job = jobQuery.data;
  const running = job?.state === "running";
  const pct = Math.round(job?.progress ?? 0);
  const report = reportQuery.data;
  const groups = duplicatesQuery.data?.groups ?? [];

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
          <Button
            variant="danger"
            onClick={() => cancel.mutate()}
            disabled={!running}
          >
            Cancel
          </Button>
        </div>
      </Card>

      {job && (
        <Card title={jobTitle}>
          <div className="flex items-center justify-between text-sm">
            <span className="capitalize text-slate-300">{job.state}</span>
            <span className="font-medium text-white">{pct}%</span>
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
          separate artists. The canonical spelling from the metadata providers
          (when found) selects the keeper and is applied on merge; otherwise
          the largest entry wins.
        </p>

        {groups.length === 0 ? (
          <p className="mt-3 text-sm text-slate-500">
            No case-insensitive duplicates found.
          </p>
        ) : (
          <div className="mt-3 space-y-2">
            {groups.map((g) => (
              <div
                key={g.name}
                className="rounded-lg border border-slate-800 p-3"
              >
                <p className="mb-1 text-xs font-medium text-slate-400">
                  {g.artists.length} artists match &quot;{g.name}&quot;
                  {g.canonical_name && (
                    <span className="ml-2 text-amber-400">
                      canonical: {g.canonical_name}
                    </span>
                  )}
                </p>
                <div className="space-y-1">
                  {g.artists.map((a, idx) => (
                    <div
                      key={a.id}
                      className="flex items-center justify-between gap-2 text-sm"
                    >
                      <span className="min-w-0 truncate text-slate-200">
                        {a.name}{" "}
                        <span className="text-xs text-slate-500">
                          ({a.track_count} tracks)
                        </span>
                        {idx === 0 && (
                          <span className="ml-1 text-xs text-emerald-400">
                            keeper
                          </span>
                        )}
                      </span>
                      {idx > 0 && (
                        <Button
                          variant="primary"
                          disabled={merge.isPending}
                          onClick={() =>
                            merge.mutate({
                              keep: g.artists[0].id,
                              remove: a.id,
                            })
                          }
                        >
                          Merge into {g.artists[0].name}
                        </Button>
                      )}
                    </div>
                  ))}
                </div>
              </div>
            ))}
          </div>
        )}
      </Card>
    </div>
  );
}
