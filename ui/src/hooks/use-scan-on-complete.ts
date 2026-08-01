import { useEffect, useRef, useCallback } from "react";
import { toast } from "sonner";
import type { DownloadRecord as DownloadItemType } from "../api/types";
import type { UseMutationResult } from "@tanstack/react-query";

interface UseScanOnCompleteOptions {
  downloads: DownloadItemType[] | undefined;
  scanLibrary: UseMutationResult<unknown, Error, void>;
}

export function useScanOnComplete({ downloads, scanLibrary }: UseScanOnCompleteOptions) {
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
        scanLibrary.mutate(undefined, {
          onSuccess: (stats: any) => {
            toast.success(
              `Library scanned: ${stats.imported} tracks imported`,
            );
          },
          onError: (err) => {
            toast.error(
              `Scan failed: ${err instanceof Error ? err.message : "Unknown error"}`,
            );
          },
        });
      }, 30_000);
    }
  }, [downloads, scanLibrary]);

  return { resetScannedIds };
}
