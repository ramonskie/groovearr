import {
  useJobState,
  useStartScanJob,
  useStartEnrichJob,
  useCancelJob,
} from "../../hooks/use-job";
import Card from "../../components/Card";
import Button from "../../components/Button";

export default function JobsSettings() {
  const jobQuery = useJobState();
  const startScan = useStartScanJob();
  const startEnrich = useStartEnrichJob();
  const cancel = useCancelJob();

  const job = jobQuery.data;
  const running = job?.state === "running";
  const pct = Math.round(job?.progress ?? 0);

  return (
    <div>
      <Card title="Background Jobs">
        <p className="text-sm text-slate-400">
          Run long-running library tasks in the background. Scan imports new or
          changed audio files into the library; Enrich fills in missing
          metadata (ISRC, genres, release dates, artwork) from the configured
          metadata providers.
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
            variant="danger"
            onClick={() => cancel.mutate()}
            disabled={!running}
          >
            Cancel
          </Button>
        </div>
      </Card>

      {job && (
        <Card title={job.type === "scan" ? "Scan" : "Enrichment"}>
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
    </div>
  );
}
