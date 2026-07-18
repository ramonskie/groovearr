import { useState, useEffect, useCallback } from "react";
import { useForm, FormProvider } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { toast } from "sonner";
import { useConfig, useUpdateConfig } from "../../hooks/use-config";
import type { ConfigUpdatePayload } from "../../api/types";
import SubTabs from "../../components/SubTabs";
import Spinner from "../../components/Spinner";
import {
  settingsFormSchema,
  settingsDefaults,
  type SettingsFormValues,
} from "./settings-schema";
import GeneralSettings from "./GeneralSettings";
import SourcesSettings from "./SourcesSettings";
import LibrarySettings from "./LibrarySettings";

const TABS = [
  { id: "general", label: "General" },
  { id: "sources", label: "Download Sources" },
  { id: "library", label: "Library" },
] as const;

type TabId = (typeof TABS)[number]["id"];

export default function SettingsPage() {
  const [activeTab, setActiveTab] = useState<TabId>("general");

  const { data: config, isLoading, error } = useConfig();
  const updateConfig = useUpdateConfig();

  const form = useForm<SettingsFormValues>({
    resolver: zodResolver(settingsFormSchema),
    defaultValues: settingsDefaults,
  });

  // Pre-fill form when config loads
  useEffect(() => {
    if (config) {
      form.reset({
        download_path: config.library.download_path ?? "",
        slskd_url: config.soulseek.slskd_url ?? "",
        slskd_api_key: config.soulseek.api_key ?? "",
        deezer_arl: config.deezer.arl ?? "",
        deezer_quality: config.deezer.quality ?? "flac",
        library_path: config.library.library_path ?? "",
        folder_template: config.library.folder_template ?? "",
        playlist_path: config.library.playlist_path ?? "",
        playlist_template: config.library.playlist_template ?? "",
      });
    }
  }, [config, form]);

  const handleSave = useCallback(
    (payload: ConfigUpdatePayload, section: string) => {
      updateConfig.mutate(payload, {
        onSuccess: () => {
          toast.success(`${section} settings saved`);
        },
        onError: (err) => {
          toast.error(err instanceof Error ? err.message : "Failed to save settings");
        },
      });
    },
    [updateConfig],
  );

  if (isLoading) {
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
          onTabChange={(id) => setActiveTab(id as TabId)}
          className="mb-6"
        />

        <div className="max-w-2xl">
          {activeTab === "general" && (
            <GeneralSettings onSave={handleSave} isSaving={updateConfig.isPending} />
          )}
          {activeTab === "sources" && (
            <SourcesSettings onSave={handleSave} isSaving={updateConfig.isPending} />
          )}
          {activeTab === "library" && (
            <LibrarySettings onSave={handleSave} isSaving={updateConfig.isPending} />
          )}
        </div>
      </div>
    </FormProvider>
  );
}
