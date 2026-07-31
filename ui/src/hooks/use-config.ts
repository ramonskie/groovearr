import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  getConfig,
  updateConfig,
  getSources,
  testConnection,
} from "../api/client";
import type {
  ConfigUpdatePayload,
} from "../api/types";

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
