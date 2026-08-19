import { useCallback, useEffect, useRef } from "react";
import type { DownloadRecord as DownloadItemType } from "../api/types";
import type { StartJobResponse } from "../api/types";
import type { UseMutationResult } from "@tanstack/react-query";

interface UseScanOnCompleteOptions {
  downloads: DownloadItemType[] | undefined;
  startScan: UseMutationResult<StartJobResponse, Error, void>;
}

export function useScanOnComplete({ downloads, startScan }: UseScanOnCompleteOptions) {
  const scannedIds = useRef<Set<string>>(new Set());
  const scanTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const resetScannedIds = useCallback(() => {
    scannedIds.current.clear();
  }, []);

  useEffect(() => {
    return () => {
      if (scanTimer.current) clearTimeout(scanTimer.current);
    };
  }, []);

  useEffect(() => {
    if (!downloads || downloads.length === 0) return;

    const newlySucceeded = downloads.filter(
      (d) => d.state === "imported" && !scannedIds.current.has(d.id),
    );

    if (newlySucceeded.length > 0) {
      if (scanTimer.current) clearTimeout(scanTimer.current);
      scanTimer.current = setTimeout(() => {
        for (const d of downloads) {
          if (d.state === "imported") scannedIds.current.add(d.id);
        }
        // Toasts for success/failure are handled by useStartScanJob.
        startScan.mutate(undefined);
      }, 30_000);
    }
  }, [downloads, startScan]);

  return { resetScannedIds };
}
