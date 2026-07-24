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

const MODE_OPTIONS = [
  { value: "free", label: "Free (no credentials)" },
  { value: "dev", label: "Developer App" },
] as const;

export default function SpotifySection({ source }: Props) {
  const {
    register,
    watch,
    formState: { errors },
  } = useFormContext<SettingsFormValues>();

  const testConnection = useTestConnection();
  const [testResult, setTestResult] = useState<TestResult | null>(null);

  const badge = source ? sourceBadge(source.status) : sourceBadge("not_configured");
  const mode = watch("spotify_mode") ?? "free";
  const isDev = mode === "dev";

  const handleTest = useCallback(async () => {
    setTestResult(null);
    try {
      await testConnection.mutateAsync("spotify");
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
      title="Spotify"
      actions={<CapabilityBadges capabilities={source?.capabilities} />}
    >
      <div className="space-y-4">
        <FormGroup
          label="Mode"
          htmlFor="spotify_mode"
          hint={
            isDev
              ? "Full Spotify API access — search, playlists, metadata. Requires a Spotify Developer App."
              : "No credentials needed. Parses Spotify URLs for metadata via public endpoints."
          }
          error={errors.spotify_mode?.message}
        >
          <select
            id="spotify_mode"
            {...register("spotify_mode")}
            className="w-full rounded-lg border border-slate-700 bg-slate-800 px-3 py-2 text-sm text-white focus:border-purple-500 focus:outline-none focus:ring-1 focus:ring-purple-500"
          >
            {MODE_OPTIONS.map((opt) => (
              <option key={opt.value} value={opt.value}>
                {opt.label}
              </option>
            ))}
          </select>
        </FormGroup>

        {isDev && (
          <>
            <FormGroup
              label="Client ID"
              htmlFor="spotify_client_id"
              hint="Your Spotify Developer App client ID."
              error={errors.spotify_client_id?.message}
            >
              <input
                id="spotify_client_id"
                type="text"
                placeholder="Enter client ID"
                {...register("spotify_client_id")}
                className="w-full rounded-lg border border-slate-700 bg-slate-800 px-3 py-2 text-sm text-white placeholder:text-slate-500 focus:border-purple-500 focus:outline-none focus:ring-1 focus:ring-purple-500"
              />
            </FormGroup>

            <FormGroup
              label="Client Secret"
              htmlFor="spotify_client_secret"
              hint="Your Spotify Developer App client secret."
              error={errors.spotify_client_secret?.message}
            >
              <input
                id="spotify_client_secret"
                type="password"
                placeholder="Enter client secret"
                {...register("spotify_client_secret")}
                className="w-full rounded-lg border border-slate-700 bg-slate-800 px-3 py-2 text-sm text-white placeholder:text-slate-500 focus:border-purple-500 focus:outline-none focus:ring-1 focus:ring-purple-500"
              />
            </FormGroup>

            <FormGroup
              label="Redirect URI"
              htmlFor="spotify_redirect_uri"
              hint="Must match the redirect URI configured in your Spotify Developer App."
              error={errors.spotify_redirect_uri?.message}
            >
              <input
                id="spotify_redirect_uri"
                type="text"
                placeholder="http://localhost:8008/api/spotify/callback"
                {...register("spotify_redirect_uri")}
                className="w-full rounded-lg border border-slate-700 bg-slate-800 px-3 py-2 text-sm text-white placeholder:text-slate-500 focus:border-purple-500 focus:outline-none focus:ring-1 focus:ring-purple-500"
              />
            </FormGroup>
          </>
        )}
      </div>

      <div className="mt-4 flex items-center gap-3">
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

        {isDev && (
          <>
            <a
              href="/api/spotify/login"
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1 rounded-lg border border-green-700 bg-green-950/50 px-3 py-2 text-sm text-green-400 hover:bg-green-900/50 transition-colors"
            >
              Connect Spotify Account
            </a>
            <a
              href="https://developer.spotify.com/dashboard"
              target="_blank"
              rel="noopener noreferrer"
              className="text-xs text-slate-500 hover:text-slate-300 transition-colors"
            >
              Developer Dashboard
            </a>
          </>
        )}
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
    </Card>
  );
}
