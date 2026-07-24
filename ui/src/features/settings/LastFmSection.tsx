import { useState, useCallback } from "react";
import { useFormContext } from "react-hook-form";
import { useTestConnection } from "../../hooks/use-config";
import type { SourceInfo } from "../../api/types";
import Card from "../../components/Card";
import FormGroup from "../../components/FormGroup";
import Button from "../../components/Button";
import CapabilityBadges from "./CapabilityBadges";
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

export default function LastFmSection({ source }: Props) {
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
      await testConnection.mutateAsync("lastfm");
      setTestResult({ ok: true, message: "Connected" });
    } catch (err) {
      setTestResult({
        ok: false,
        message: err instanceof Error ? err.message : "Connection failed",
      });
    }
  }, [testConnection]);

  return (
    <Card
      title="Last.fm"
      actions={<CapabilityBadges capabilities={source?.capabilities} />}
    >
      <div className="space-y-4">
        <FormGroup
          label="API Key"
          htmlFor="lastfm_api_key"
          hint="Free API key from last.fm/api/account/create. Required for artist images, metadata, and discovery."
          error={errors.lastfm_api_key?.message}
        >
          <input
            id="lastfm_api_key"
            type="text"
            placeholder="Your Last.fm API key"
            {...register("lastfm_api_key")}
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
