import { useState, useCallback } from "react";
import { useFormContext } from "react-hook-form";
import { useTestConnection } from "../../hooks/use-config";
import type { ConfigUpdatePayload, SourceInfo } from "../../api/types";
import Card from "../../components/Card";
import FormGroup from "../../components/FormGroup";
import Button from "../../components/Button";
import Badge from "../../components/Badge";
import Spinner from "../../components/Spinner";
import {
  toSoulseekPayload,
  sourceBadge,
  type SettingsFormValues,
} from "./settings-schema";

interface Props {
  source?: SourceInfo;
  onSave: (payload: ConfigUpdatePayload, section: string) => void;
  isSaving: boolean;
}

interface TestResult {
  ok: boolean;
  message: string;
}

export default function SoulseekSection({ source, onSave, isSaving }: Props) {
  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useFormContext<SettingsFormValues>();

  const testConnection = useTestConnection();
  const [testResult, setTestResult] = useState<TestResult | null>(null);

  const badge = source ? sourceBadge(source.status) : sourceBadge("not_configured");

  const onSubmit = (values: SettingsFormValues) => {
    onSave(toSoulseekPayload(values), "Soulseek");
  };

  const handleTest = useCallback(async () => {
    setTestResult(null);
    try {
      await testConnection.mutateAsync("soulseek");
      setTestResult({ ok: true, message: "✓ Connected" });
    } catch (err) {
      setTestResult({
        ok: false,
        message: err instanceof Error ? err.message : "Connection failed",
      });
    }
  }, [testConnection]);

  return (
    <Card
      title="Soulseek (slskd)"
      actions={<Badge variant={badge.variant}>{badge.label}</Badge>}
    >
      <form onSubmit={handleSubmit(onSubmit)}>
        <div className="space-y-4">
          <FormGroup
            label="slskd URL"
            htmlFor="slskd_url"
            hint="Full URL to your slskd instance (e.g. https://slskd.example.com:5030)."
            error={errors.slskd_url?.message}
          >
            <input
              id="slskd_url"
              type="text"
              placeholder="https://slskd.example.com:5030"
              {...register("slskd_url")}
              className="w-full rounded-lg border border-slate-700 bg-slate-800 px-3 py-2 text-sm text-white placeholder:text-slate-500 focus:border-purple-500 focus:outline-none focus:ring-1 focus:ring-purple-500"
            />
          </FormGroup>

          <FormGroup
            label="API Key"
            htmlFor="slskd_api_key"
            hint="Your slskd API key from the slskd web interface."
            error={errors.slskd_api_key?.message}
          >
            <input
              id="slskd_api_key"
              type="password"
              placeholder="Enter API key"
              {...register("slskd_api_key")}
              className="w-full rounded-lg border border-slate-700 bg-slate-800 px-3 py-2 text-sm text-white placeholder:text-slate-500 focus:border-purple-500 focus:outline-none focus:ring-1 focus:ring-purple-500"
            />
          </FormGroup>
        </div>

        {testResult && (
          <div
            className={`mt-3 flex items-center gap-2 rounded-lg border p-3 text-sm ${
              testResult.ok
                ? "border-green-800 bg-green-950/50 text-green-300"
                : "border-red-800 bg-red-950/50 text-red-300"
            }`}
          >
            {testResult.message}
          </div>
        )}

        <div className="mt-4 flex items-center gap-3">
          <Button type="submit" loading={isSaving}>
            Save
          </Button>
          <Button
            type="button"
            variant="ghost"
            loading={testConnection.isPending}
            onClick={handleTest}
          >
            {testConnection.isPending ? (
              <>
                <Spinner size="sm" />
                Testing...
              </>
            ) : (
              "Test Connection"
            )}
          </Button>
        </div>
      </form>
    </Card>
  );
}
