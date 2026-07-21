import { useState, useCallback } from "react";
import { useFormContext } from "react-hook-form";
import { useTestConnection } from "../../hooks/use-config";
import type { SourceInfo } from "../../api/types";
import Card from "../../components/Card";
import FormGroup from "../../components/FormGroup";
import Button from "../../components/Button";
import Badge from "../../components/Badge";
import Spinner from "../../components/Spinner";
import {
  sourceBadge,
  type SettingsFormValues,
} from "./settings-schema";

interface Props {
  source?: SourceInfo;
}

interface TestResult {
  ok: boolean;
  message: string;
}

const QUALITY_OPTIONS = [
  { value: "flac", label: "FLAC Lossless" },
  { value: "mp3_320", label: "MP3 320kbps" },
  { value: "mp3_128", label: "MP3 128kbps" },
] as const;

export default function DeezerSection({ source }: Props) {
  const {
    register,
    formState: { errors },
  } = useFormContext<SettingsFormValues>();

  const testConnection = useTestConnection();
  const [testResult, setTestResult] = useState<TestResult | null>(null);

  const badge = source ? sourceBadge(source.status) : sourceBadge("not_configured");

  const handleTest = useCallback(async () => {
    setTestResult(null);
    try {
      const result = await testConnection.mutateAsync("deezer");
      if (result.error) {
        setTestResult({ ok: false, message: result.error });
      } else {
        setTestResult({ ok: true, message: "✓ Connected" });
      }
    } catch (err) {
      setTestResult({
        ok: false,
        message: err instanceof Error ? err.message : "Connection failed",
      });
    }
  }, [testConnection]);

  return (
    <Card
      title="Deezer"
      actions={<Badge variant={badge.variant}>{badge.label}</Badge>}
    >
      <div className="space-y-4">
        <FormGroup
          label="ARL"
          htmlFor="deezer_arl"
          hint="Your Deezer ARL token for authentication."
          error={errors.deezer_arl?.message}
        >
          <input
            id="deezer_arl"
            type="password"
            placeholder="Enter ARL token"
            {...register("deezer_arl")}
            className="w-full rounded-lg border border-slate-700 bg-slate-800 px-3 py-2 text-sm text-white placeholder:text-slate-500 focus:border-purple-500 focus:outline-none focus:ring-1 focus:ring-purple-500"
          />
        </FormGroup>

        <FormGroup
          label="Quality"
          htmlFor="deezer_quality"
          hint="Preferred download quality for Deezer tracks."
          error={errors.deezer_quality?.message}
        >
          <select
            id="deezer_quality"
            {...register("deezer_quality")}
            className="w-full rounded-lg border border-slate-700 bg-slate-800 px-3 py-2 text-sm text-white focus:border-purple-500 focus:outline-none focus:ring-1 focus:ring-purple-500"
          >
            {QUALITY_OPTIONS.map((opt) => (
              <option key={opt.value} value={opt.value}>
                {opt.label}
              </option>
            ))}
          </select>
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

      <div className="mt-4">
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
    </Card>
  );
}
