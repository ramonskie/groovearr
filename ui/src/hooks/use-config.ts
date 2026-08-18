import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  getConfig,
  updateConfig,
  getSources,
  testConnection,
  getSetupStatus,
} from "../api/client";
import type {
  ConfigUpdatePayload,
} from "../api/types";

// ─── Setup status (first-run wizard) ───────────────────────────────

export function useSetupStatus() {
  return useQuery({
    queryKey: ["setup-status"] as const,
    queryFn: getSetupStatus,
    staleTime: 30_000,
  });
}

// ─── Config ─────────────────────────────────────────────────────────

export function useConfig() {
  return useQuery({
    queryKey: ["config"] as const,
    queryFn: getConfig,
  });
}

export function useUpdateConfig() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (payload: ConfigUpdatePayload) => updateConfig(payload),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["config"] });
      queryClient.invalidateQueries({ queryKey: ["sources"] });
      queryClient.invalidateQueries({ queryKey: ["setup-status"] });
    },
  });
}

// ─── Sources ────────────────────────────────────────────────────────

export function useSources() {
  return useQuery({
    queryKey: ["sources"] as const,
    queryFn: getSources,
  });
}

export function useTestConnection() {
  return useMutation({
    mutationFn: (source: string) => testConnection(source),
  });
}
