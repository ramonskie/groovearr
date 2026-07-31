import { useEffect, useRef, useCallback, useMemo } from "react";
import { useForm, FormProvider } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { useSearchParams } from "react-router-dom";
import { useConfig, useUpdateConfig, useSources } from "../../hooks/use-config";
import SubTabs from "../../components/SubTabs";
import Spinner from "../../components/Spinner";
import {
  buildFormSchema,
  buildDefaults,
  type SettingsFormValues,
} from "./settings-schema";
import GeneralSettings from "./GeneralSettings";
import SourcesSettings from "./SourcesSettings";
import LibrarySettings from "./LibrarySettings";
import SecuritySettings from "./SecuritySettings";
import QualitySettings from "./QualitySettings";

const TABS = [
  { id: "general", label: "General" },
  { id: "sources", label: "Download Sources" },
  { id: "quality", label: "Quality" },
  { id: "library", label: "Library" },
  { id: "security", label: "Security" },
] as const;

type TabId = (typeof TABS)[number]["id"];

const AUTO_SAVE_MS = 1000;

export default function SettingsPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const activeTab: TabId = (() => {
    const fromParam = searchParams.get("tab");
    if (fromParam && TABS.some((t) => t.id === fromParam)) return fromParam as TabId;
    return searchParams.get("spotify") === "connected" ? "sources" : "general";
  })();

  const { data: config, isLoading: configLoading, error } = useConfig();
  const { data: sources, isLoading: sourcesLoading } = useSources();
  const updateConfig = useUpdateConfig();

  const sourceList = sources ?? [];

  const formSchema = useMemo(() => buildFormSchema(sourceList), [sourceList]);
  const defaultValues = useMemo(() => buildDefaults(sourceList), [sourceList]);

  const form = useForm<SettingsFormValues>({
    resolver: zodResolver(formSchema),
    defaultValues,
  });

  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Pre-fill form when config + sources load. On subsequent refetches
  // (e.g. after auto-save), only fields the user hasn't edited are reset.
  // This keeps download_order / metadata_order in sync with backend cleanup
  // while preserving in-progress user edits on source config fields.
  useEffect(() => {
    if (config && sourceList.length > 0) {
      const sourceValues: Record<string, Record<string, string>> = {};
      for (const s of sourceList) {
        const cfgFields: Record<string, string> = {};
        const pluginCfg = (config.sources?.[s.name] ?? {}) as Record<string, unknown>;
        for (const field of s.config_schema ?? []) {
          cfgFields[field.name] = String(pluginCfg[field.name] ?? field.default ?? "");
        }
        sourceValues[s.name] = cfgFields;
      }

      form.reset(
        {
          download_path: config.library.download_path ?? "",
          sources: sourceValues,
          library_path: config.library.library_path ?? "",
          folder_template: config.library.folder_template ?? "",
          playlist_path: config.library.playlist_path ?? "",
          playlist_template: config.library.playlist_template ?? "",
          playlist_auto_sync_mins: config.library.playlist_auto_sync_mins ?? undefined,
          auth_method: (config.auth?.method || "none") as "none" | "forms",
          auth_username: config.auth?.username ?? "",
          auth_password: "",
          auth_api_key: config.auth?.api_key ?? "",
          auth_local_bypass_subnets: (config.auth?.local_bypass_subnets ?? []).join("\n"),
          metadata_order: config.metadata_order ?? [],
          download_order: config.download_order ?? [],
        },
        { keepDirtyValues: true },
      );
    }
  }, [config, sourceList, form]);

  // Auto-save on form change with debounce
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
              // Toggle fields must be sent as booleans, not strings.
              const fieldType = sourceSchema.find((f) => f.name === k)?.type;
              if (fieldType === "toggle") {
                cleaned[k] = v === "true";
              } else {
                cleaned[k] = v ?? "";
              }
            }
            // Preserve server-managed keys (OAuth tokens, etc.) that are
            // not part of the config schema and not user-editable.
            const existing = (config?.sources?.[name] ?? {}) as Record<string, unknown>;
            const schemaFields = new Set(
              (sourceList.find((s) => s.name === name)?.config_schema ?? []).map((f) => f.name),
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

  if (configLoading || sourcesLoading) {
    return (
      <div className="flex items-center justify-center py-16">
        <Spinner size="lg" />
      </div>
    );
  }

  if (error) {
    return (
      <div className="py-8 text-center text-red-400">
        Failed to load settings. Please try again.
      </div>
    );
  }

  return (
    <FormProvider {...form}>
      <div>
        <h2 className="mb-4 text-xl font-bold text-white">Settings</h2>

        <SubTabs
          tabs={TABS.map((t) => ({ id: t.id, label: t.label }))}
          activeTab={activeTab}
          onTabChange={(id) => {
            setSearchParams((prev) => {
              const next = new URLSearchParams(prev);
              next.set("tab", id);
              next.delete("spotify");
              return next;
            });
          }}
          className="mb-6"
        />

        <div className="max-w-2xl">
          {activeTab === "general" && <GeneralSettings />}
          {activeTab === "sources" && <SourcesSettings />}
          {activeTab === "library" && <LibrarySettings />}
          {activeTab === "security" && <SecuritySettings />}
        </div>
        {activeTab === "quality" && <QualitySettings />}
      </div>
    </FormProvider>
  );
}
