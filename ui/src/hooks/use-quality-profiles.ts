import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  getQualityProfiles,
  createQualityProfile,
  updateQualityProfile,
  deleteQualityProfile,
  setDefaultQualityProfile,
  getQualityPresets,
} from "../api/client";
import type {
  QualityProfileCreatePayload,
  QualityProfileUpdatePayload,
} from "../api/types";

const PROFILES_KEY = ["quality-profiles"] as const;

export function useQualityProfiles() {
  return useQuery({
    queryKey: PROFILES_KEY,
    queryFn: getQualityProfiles,
  });
}

export function useCreateQualityProfile() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (payload: QualityProfileCreatePayload) =>
      createQualityProfile(payload),
    onSuccess: () => qc.invalidateQueries({ queryKey: PROFILES_KEY }),
  });
}

export function useUpdateQualityProfile() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      id,
      payload,
    }: {
      id: number;
      payload: QualityProfileUpdatePayload;
    }) => updateQualityProfile(id, payload),
    onSuccess: () => qc.invalidateQueries({ queryKey: PROFILES_KEY }),
  });
}

export function useDeleteQualityProfile() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => deleteQualityProfile(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: PROFILES_KEY }),
  });
}

export function useSetDefaultQualityProfile() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => setDefaultQualityProfile(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: PROFILES_KEY }),
  });
}

export function useQualityPresets() {
  return useQuery({
    queryKey: [...PROFILES_KEY, "presets"],
    queryFn: getQualityPresets,
    staleTime: Infinity,
  });
}
