import { useState, useEffect, useRef, useCallback } from "react";
import { useForm, FormProvider } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { useSearchParams } from "react-router-dom";
import { useConfig, useUpdateConfig } from "../../hooks/use-config";
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
  const [searchParams] = useSearchParams();
  const [activeTab, setActiveTab] = useState<TabId>(
    searchParams.get("spotify") === "connected" ? "sources" : "general"
  );

  const { data: config, isLoading, error } = useConfig();
  const updateConfig = useUpdateConfig();

  const form = useForm<SettingsFormValues>({
    resolver: zodResolver(settingsFormSchema),
    defaultValues: settingsDefaults,
  });

  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Pre-fill form when config loads
  useEffect(() => {
    if (config) {
      const slskd = config.sources?.soulseek ?? {};
      const dz = config.sources?.deezer ?? {};
      const mb = config.sources?.musicbrainz ?? {};
      const sp = config.sources?.spotify ?? {};
      const spCast = sp as { mode?: string; client_id?: string; client_secret?: string; redirect_uri?: string };
      form.reset({
        download_path: config.library.download_path ?? "",
        slskd_url: (slskd as Record<string, string>).slskd_url ?? "",
        slskd_api_key: (slskd as Record<string, string>).api_key ?? "",
        deezer_arl: (dz as Record<string, string>).arl ?? "",
        deezer_quality: ((dz as Record<string, string>).quality as "flac" | "mp3_320" | "mp3_128") ?? "flac",
        musicbrainz_email: (mb as Record<string, string>).email ?? "",
        spotify_mode: (spCast.mode as "free" | "dev") ?? "free",
        spotify_client_id: spCast.client_id ?? "",
        spotify_client_secret: spCast.client_secret ?? "",
        spotify_redirect_uri: spCast.redirect_uri ?? "",
        library_path: config.library.library_path ?? "",
        folder_template: config.library.folder_template ?? "",
        playlist_path: config.library.playlist_path ?? "",
        playlist_template: config.library.playlist_template ?? "",
        auth_method: (config.auth?.method || "none") as "none" | "forms",
        auth_username: config.auth?.username ?? "",
        auth_password: "",
        auth_api_key: config.auth?.api_key ?? "",
        auth_local_bypass_subnets: (config.auth?.local_bypass_subnets ?? []).join("\n"),
        metadata_order: config.metadata_order ?? [],
        download_order: config.download_order ?? [],
      });
    }
  }, [config, form]);

  // Auto-save on form change with debounce
  const saveValues = useCallback(
    (values: SettingsFormValues) => {
      updateConfig.mutate({
        sources: {
          soulseek: {
            slskd_url: values.slskd_url ?? "",
            api_key: values.slskd_api_key ?? "",
          },
          deezer: {
            arl: values.deezer_arl ?? "",
            quality: values.deezer_quality ?? "flac",
          },
          musicbrainz: {
            email: values.musicbrainz_email ?? "",
          },
          spotify: {
            mode: values.spotify_mode ?? "free",
            client_id: values.spotify_client_id ?? "",
            client_secret: values.spotify_client_secret ?? "",
            redirect_uri: values.spotify_redirect_uri ?? "",
            // Preserve tokens obtained via OAuth — they are server-managed, not user-editable.
            tokens: (config?.sources?.spotify as Record<string, unknown>)?.tokens ?? {},
          },
        },
        library: {
          download_path: values.download_path ?? "",
          library_path: values.library_path ?? "",
          folder_template: values.folder_template ?? "",
          playlist_path: values.playlist_path ?? "",
          playlist_template: values.playlist_template ?? "",
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
    [updateConfig, config],
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
