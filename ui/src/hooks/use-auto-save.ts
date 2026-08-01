import { useEffect, useRef, useCallback } from "react";
import type { UseFormReturn } from "react-hook-form";
import type { UseMutationResult } from "@tanstack/react-query";
import type { ConfigUpdatePayload, UpdateConfigResponse, SourceInfo, ConfigField } from "../api/types";
import type { SettingsFormValues } from "../features/settings/settings-schema";

const AUTO_SAVE_MS = 1000;

interface UseAutoSaveOptions {
  form: UseFormReturn<SettingsFormValues>;
  updateConfig: UseMutationResult<UpdateConfigResponse, Error, ConfigUpdatePayload>;
  config: ConfigUpdatePayload | undefined;
  sourceList: SourceInfo[];
}

export function useAutoSave({ form, updateConfig, config, sourceList }: UseAutoSaveOptions) {
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const saveValues = useCallback(
    (values: SettingsFormValues) => {
      const sourcesPayload: Record<string, Record<string, unknown>> = {};
      if (values.sources) {
        for (const [name, fields] of Object.entries(values.sources)) {
          if (fields && typeof fields === "object") {
            const cleaned: Record<string, unknown> = {};
            const sourceSchema =
              sourceList.find((s) => s.name === name)?.config_schema ?? [];
            for (const [k, v] of Object.entries(fields)) {
              const fieldType = sourceSchema.find((f: ConfigField) => f.name === k)?.type;
              if (fieldType === "toggle") {
                cleaned[k] = v === "true";
              } else {
                cleaned[k] = v ?? "";
              }
            }
            const existing = (config?.sources?.[name] ?? {}) as Record<string, unknown>;
            const schemaFields = new Set(
              (sourceList.find((s) => s.name === name)?.config_schema ?? []).map((f: ConfigField) => f.name),
            );
            for (const [k, v] of Object.entries(existing)) {
              if (!schemaFields.has(k) && v !== undefined) {
                cleaned[k] = v;
              }
            }
            sourcesPayload[name] = cleaned;
          }
        }
      }

      updateConfig.mutate({
        sources: sourcesPayload,
        library: {
          download_path: values.download_path ?? "",
          library_path: values.library_path ?? "",
          folder_template: values.folder_template ?? "",
          playlist_path: values.playlist_path ?? "",
          playlist_template: values.playlist_template ?? "",
          playlist_auto_sync_mins: values.playlist_auto_sync_mins,
        },
        auth: {
          method: values.auth_method ?? "none",
          username: values.auth_username ?? "",
          password: (values.auth_password?.length ?? 0) >= 4 ? values.auth_password : "",
          local_bypass_subnets: (values.auth_local_bypass_subnets ?? "")
            .split("\n")
            .map((s) => s.trim())
            .filter((s) => s.length > 0),
        },
        metadata_order: values.metadata_order,
        download_order: values.download_order,
      });
    },
    [updateConfig, config, sourceList],
  );

  useEffect(() => {
    const sub = form.watch((values) => {
      if (timerRef.current) clearTimeout(timerRef.current);
      timerRef.current = setTimeout(() => {
        saveValues(values as SettingsFormValues);
      }, AUTO_SAVE_MS);
    });
    return () => sub.unsubscribe();
  }, [form, saveValues]);

  // Cleanup timer on unmount.
  useEffect(() => {
    return () => {
      if (timerRef.current) clearTimeout(timerRef.current);
    };
  }, []);
}
