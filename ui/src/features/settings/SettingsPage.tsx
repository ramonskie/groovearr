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

const TABS = [
  { id: "general", label: "General" },
  { id: "sources", label: "Download Sources" },
  { id: "quality", label: "Quality" },
  { id: "library", label: "Library" },
  { id: "security", label: "Security" },
] as const;

type TabId = (typeof TABS)[number]["id"];

function useActiveTab(searchParams: URLSearchParams): TabId {
  const fromParam = searchParams.get("tab");
  if (fromParam && TABS.some((t) => t.id === fromParam)) return fromParam as TabId;
  return searchParams.get("spotify") === "connected" ? "sources" : "general";
}

export default function SettingsPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const activeTab = useActiveTab(searchParams);

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
          auth_api_key: config.auth?.api_key ?? "",
          auth_local_bypass_subnets: (config.auth?.local_bypass_subnets ?? []).join("\n"),
          metadata_order: config.metadata_order ?? [],
          download_order: config.download_order ?? [],
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
