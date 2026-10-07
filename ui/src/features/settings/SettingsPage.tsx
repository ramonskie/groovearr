import { useEffect, useMemo } from "react";
import { useForm, FormProvider } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { useSearchParams } from "react-router-dom";
import { useConfig, useUpdateConfig, useSources } from "../../hooks/use-config";
import { useAutoSave } from "../../hooks/use-auto-save";
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
import JobsSettings from "./JobsSettings";
import TrackingSettings from "./TrackingSettings";
import LogsSettings from "./LogsSettings";
import UsersSection from "./UsersSection";
import { useAuth } from "../../context/AuthContext";

const BASE_TABS = [
  { id: "general", label: "General" },
  { id: "sources", label: "Download Sources" },
  { id: "quality", label: "Quality" },
  { id: "library", label: "Library" },
  { id: "jobs", label: "Jobs" },
  { id: "tracking", label: "Tracking" },
  { id: "security", label: "Security" },
  { id: "logs", label: "Logs" },
] as const;

const USERS_TAB = { id: "users", label: "Users" } as const;

const ALL_TABS = [...BASE_TABS, USERS_TAB] as const;

type TabId = (typeof ALL_TABS)[number]["id"];

function useActiveTab(searchParams: URLSearchParams): TabId {
  const fromParam = searchParams.get("tab");
  if (fromParam && ALL_TABS.some((t) => t.id === fromParam)) {
    return fromParam as TabId;
  }
  return searchParams.get("spotify") === "connected" ? "sources" : "general";
}

export default function SettingsPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const activeTab = useActiveTab(searchParams);
  const { isAdmin } = useAuth();

  // Users tab is admin-only: filtered from the tab strip and guarded below so
  // a non-admin never mounts the section (server routes are adminOnly too).
  const tabs: ReadonlyArray<{ id: TabId; label: string }> = isAdmin
    ? ALL_TABS
    : BASE_TABS;

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

  // Pre-fill form when config + sources load. On subsequent refetches
  // (e.g. after auto-save), only fields the user hasn't edited are reset.
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
          auth_local_bypass_subnets: (config.auth?.local_bypass_subnets ?? []).join("\n"),
          metadata_order: config.metadata_order ?? [],
          download_order: config.download_order ?? [],
          album_sources: config.album_sources ?? [],
          download_client: config.download_client ?? "",
          tracking_refresh_mins:
            config.tracking === undefined
              ? 720
              : (config.tracking.refresh_mins ?? 0),
          tracking_auto_search_missing: config.tracking?.auto_search_missing ?? false,
          log_level: (config.logging?.level ?? "info") as "info" | "debug" | "warn" | "error",
          log_format: (config.logging?.format ?? "json") as "text" | "json",
          log_max_size_mb: config.logging?.max_size_mb ?? undefined,
          log_max_backups: config.logging?.max_backups ?? undefined,
          log_max_age_days: config.logging?.max_age_days ?? undefined,
          log_compress: config.logging?.compress ?? true,
          log_access_log: config.logging?.access_log ?? false,
          log_captured_max: config.logging?.captured_max ?? 2000,
        },
        { keepDirtyValues: true },
      );
    }
  }, [config, sourceList, form]);

  useAutoSave({ form, updateConfig, config, sourceList });

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
          tabs={tabs.map((t) => ({ id: t.id, label: t.label }))}
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
          {activeTab === "jobs" && <JobsSettings />}
          {activeTab === "tracking" && <TrackingSettings />}
          {activeTab === "security" && <SecuritySettings />}
          {activeTab === "logs" && <LogsSettings />}
        </div>
        {activeTab === "quality" && <QualitySettings />}
        {activeTab === "users" && isAdmin && <UsersSection />}
      </div>
    </FormProvider>
  );
}
