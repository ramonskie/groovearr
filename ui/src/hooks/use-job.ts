import { useEffect, useRef } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  getJob,
  startScanJob,
  startEnrichJob,
  startOrganizeJob,
  startDuplicatesJob,
  cancelJob,
} from "../api/client";

/**
 * Shared query for the current background job. Polls /api/jobs once per
 * second while a job is running.
 *
 * GET /api/jobs is admin-only, so callers must pass `enabled` (normally their
 * `isAdmin`), and a non-admin gets no request. Gating via React Query's
 * `enabled` — rather than `enabled && useQuery(...)` at the call site — keeps
 * the hook call order stable (Rules of Hooks).
 */
export function useJobState(enabled: boolean) {
  return useQuery({
    queryKey: ["jobs", "current"] as const,
    queryFn: getJob,
    enabled,
    refetchInterval: (q) => (q.state.data?.state === "running" ? 1000 : false),
    staleTime: 0,
  });
}

/**
 * Global job watcher. Keeps the job query alive app-wide and, when a running
 * job transitions to a terminal state, refreshes the library cache and shows
 * a toast. Mount exactly once (e.g. in the app shell) so the completion
 * effect fires exactly once no matter which page is open — otherwise a scan
 * started from Settings would leave the Library page stale for up to 30m.
 *
 * `enabled` gates the admin-only /api/jobs poll; a non-admin never issues the
 * request and the completion effect stays inert (no data => no transition).
 *
 * Only jobs observed as `running` and then terminal are handled here; jobs
 * that finish before this watcher samples them are handled by useStartJob,
 * which knows the response was for a start it just issued.
 */
export function useJobWatcher(enabled: boolean) {
  const queryClient = useQueryClient();
  const prevStateRef = useRef<string | undefined>(undefined);
  const query = useJobState(enabled);
  const job = query.data;

  useEffect(() => {
    const prev = prevStateRef.current;
    const state = job?.state;
    if (!state) return;

    if (prev === "running" && state !== "running") {
      // A cancelled scan may still have imported partial files.
      queryClient.invalidateQueries({ queryKey: ["library"] });
      // Organize writes its report only on completion — refetch it.
      queryClient.invalidateQueries({ queryKey: ["organize"] });
      // A finished duplicate check refreshes the duplicates list.
      queryClient.invalidateQueries({ queryKey: ["artists", "duplicates"] });
      if (state === "completed") {
        toast.success(
          job?.message ||
            (job?.type === "scan"
              ? "Library scan complete"
              : job?.type === "duplicates"
                ? "Duplicate check complete"
                : job?.type === "sync"
                  ? "Playlist sync complete"
                  : "Library enrichment complete"),
        );
      } else if (state === "failed") {
        toast.error(`Job failed: ${job?.error ?? "unknown error"}`);
      } else if (state === "cancelled") {
        toast.info("Job cancelled");
      }
    }
    prevStateRef.current = state;
  }, [job, queryClient]);

  return query;
}

// ─── Mutations ──────────────────────────────────────────────────────

function useStartJob(kind: "scan" | "enrich") {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: kind === "scan" ? startScanJob : startEnrichJob,
    onSuccess: ({ job, started }) => {
      queryClient.setQueryData(["jobs", "current"], job);
      if (!started) return; // job already running — nothing this request did
      if (job.state === "running") {
        toast.info(
          kind === "scan" ? "Library scan started" : "Library enrichment started",
        );
        return;
      }
      // The job finished before the response returned — report the outcome
      // here; the watcher only handles jobs it observed as running.
      queryClient.invalidateQueries({ queryKey: ["library"] });
      if (job.state === "completed") {
        toast.success(
          job.message ||
            (kind === "scan"
              ? "Library scan complete"
              : "Library enrichment complete"),
        );
      } else if (job.state === "failed") {
        toast.error(`Job failed: ${job.error ?? "unknown error"}`);
      } else if (job.state === "cancelled") {
        toast.info("Job cancelled");
      }
    },
    onError: (err) => {
      toast.error(
        `Failed to start ${kind === "scan" ? "scan" : "enrichment"}: ${
          err instanceof Error ? err.message : "Unknown error"
        }`,
      );
    },
  });
}

export function useStartScanJob() {
  return useStartJob("scan");
}

export function useStartEnrichJob() {
  return useStartJob("enrich");
}

/**
 * Starts the folder-template organize job. `mutate(true)` = dry run (report
 * only), `mutate(false)` = repair (moves files).
 */
export function useStartOrganizeJob() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (dryRun: boolean) => startOrganizeJob(dryRun),
    onSuccess: ({ job, started }) => {
      queryClient.setQueryData(["jobs", "current"], job);
      if (!started) return;
      queryClient.invalidateQueries({ queryKey: ["organize"] });
      if (job.state === "running") {
        toast.info("Library organize started");
        return;
      }
      queryClient.invalidateQueries({ queryKey: ["library"] });
      if (job.state === "completed") {
        toast.success(job.message || "Library organize complete");
      } else if (job.state === "failed") {
        toast.error(`Job failed: ${job.error ?? "unknown error"}`);
      } else if (job.state === "cancelled") {
        toast.info("Job cancelled");
      }
    },
    onError: (err) => {
      toast.error(
        `Failed to start organize: ${
          err instanceof Error ? err.message : "Unknown error"
        }`,
      );
    },
  });
}

/**
 * Starts the duplicate-artist check job. The duplicates list is populated
 * from the persisted scan once the job completes.
 */
export function useStartDuplicatesJob() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: startDuplicatesJob,
    onSuccess: ({ job, started }) => {
      queryClient.setQueryData(["jobs", "current"], job);
      if (!started) return; // job already running
      if (job.state === "running") {
        toast.info("Duplicate check started");
        return;
      }
      // Finished before the response returned — report the outcome here.
      queryClient.invalidateQueries({ queryKey: ["artists", "duplicates"] });
      if (job.state === "completed") {
        toast.success(job.message || "Duplicate check complete");
      } else if (job.state === "failed") {
        toast.error(`Job failed: ${job.error ?? "unknown error"}`);
      } else if (job.state === "cancelled") {
        toast.info("Job cancelled");
      }
    },
    onError: (err) => {
      toast.error(
        `Failed to start duplicate check: ${
          err instanceof Error ? err.message : "Unknown error"
        }`,
      );
    },
  });
}

export function useCancelJob() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: cancelJob,
    onSuccess: (job) => {
      queryClient.setQueryData(["jobs", "current"], job);
    },
  });
}
