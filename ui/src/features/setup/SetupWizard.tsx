import { useEffect, useMemo, useState } from "react";
import { useForm, FormProvider } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { useNavigate } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { useConfig, useUpdateConfig, useSources } from "../../hooks/use-config";
import { useAutoSave } from "../../hooks/use-auto-save";
import Card from "../../components/Card";
import Button from "../../components/Button";
import Spinner from "../../components/Spinner";
import ProviderSection from "../settings/ProviderSection";
import SecuritySettings from "../settings/SecuritySettings";
import DownloadOrderSection from "../settings/DownloadOrderSection";
import SourceRoutingSection from "../settings/SourceRoutingSection";
import {
  buildFormSchema,
  buildDefaults,
  type SettingsFormValues,
} from "../settings/settings-schema";

export default function SetupWizard() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [step, setStep] = useState(0);

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
        },
        { keepDirtyValues: true },
      );
    }
  }, [config, sourceList, form]);

  useAutoSave({ form, updateConfig, config, sourceList });

  const finishSetup = (target: string) => {
    // Optimistically mark setup done so SetupGate stops redirecting to /setup
    // immediately, before the async refetch from the PUT resolves.
    queryClient.setQueryData(["setup-status"], { needs_setup: false });
    updateConfig.mutate(
      { setup_completed: true },
      {
        onSuccess: () => navigate(target),
        onError: (err) => {
          toast.error(err instanceof Error ? err.message : "Failed to save setup");
          queryClient.invalidateQueries({ queryKey: ["setup-status"] });
        },
      },
    );
  };

  if (configLoading || sourcesLoading) {
    return (
      <div className="flex items-center justify-center py-24">
        <Spinner size="lg" />
      </div>
    );
  }

  if (error) {
    return (
      <div className="py-16 text-center text-red-400">
        Failed to load settings: {error.message}
      </div>
    );
  }

  const downloadSources = sourceList
    .filter((s) => (s.config_schema && s.config_schema.length > 0) || s.oauth?.enabled)
    .sort((a, b) => a.display_name.localeCompare(b.display_name));

  return (
    <div className="mx-auto max-w-3xl px-4 py-8">
      <div className="mb-8">
        <h1 className="text-2xl font-bold text-white">Welcome to Groovearr</h1>
        <p className="mt-1 text-sm text-slate-400">
          Set up your first download source to get started. You can change
          everything later in Settings.
        </p>
      </div>

      {/* Step indicator */}
      <div className="mb-6 flex items-center gap-2 text-xs">
        {["Security", "Sources", "Routing", "Done"].map((label, i) => (
          <div key={label} className="flex items-center gap-2">
            {i > 0 && <span className="text-slate-600">→</span>}
            <span
              className={
                i === step
                  ? "font-semibold text-purple-400"
                  : i < step
                    ? "text-green-400"
                    : "text-slate-500"
              }
            >
              {i + 1}. {label}
            </span>
          </div>
        ))}
      </div>

      <FormProvider {...form}>
        {step === 0 && (
          <div className="space-y-4">
            <SecuritySettings />
            <div className="flex items-center justify-between">
              <span className="text-xs text-slate-500">
                Choose None for no login, or Forms to protect Groovearr.
              </span>
              <Button variant="primary" onClick={() => setStep(1)}>
                Continue
              </Button>
            </div>
          </div>
        )}

        {step === 1 && (
          <div className="space-y-4">
            <Card title="Add a download source">
              <p className="mb-4 text-sm text-slate-400">
                The free option is <strong className="text-white">Soulseek</strong> —
                flip its toggle on, fill in the slskd URL and API key, and click
                Test Connection.
              </p>
              <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
                {downloadSources.map((source) => (
                  <ProviderSection key={source.name} source={source} />
                ))}
              </div>
            </Card>
            <div className="flex items-center justify-between">
              <Button variant="ghost" onClick={() => setStep(0)}>
                Back
              </Button>
              <div className="flex items-center gap-2">
                <Button variant="ghost" onClick={() => finishSetup("/discover")}>
                  Skip for now
                </Button>
                <Button variant="primary" onClick={() => setStep(2)}>
                  Continue
                </Button>
              </div>
            </div>
          </div>
        )}

        {step === 2 && (
          <div className="space-y-4">
            <SourceRoutingSection />
            <DownloadOrderSection />
            <div className="flex items-center justify-between">
              <Button variant="ghost" onClick={() => setStep(1)}>
                Back
              </Button>
              <Button variant="primary" onClick={() => setStep(3)}>
                Continue
              </Button>
            </div>
          </div>
        )}

        {step === 3 && (
          <Card title="You're all set">
            <p className="text-sm text-slate-400">
              Your changes are saved. Head to Discover to search and download your
              first album.
            </p>
            <div className="mt-6 flex justify-end">
              <Button variant="primary" onClick={() => finishSetup("/discover")}>
                Start downloading
              </Button>
            </div>
          </Card>
        )}
      </FormProvider>
    </div>
  );
}
