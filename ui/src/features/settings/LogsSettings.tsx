import { useFormContext } from "react-hook-form";
import Card from "../../components/Card";
import FormGroup from "../../components/FormGroup";
import Spinner from "../../components/Spinner";
import { useLogStream, logEntryKey } from "../../hooks/use-logs";
import type { SettingsFormValues } from "./settings-schema";

const LEVEL_COLORS: Record<string, string> = {
  DEBUG: "text-slate-400",
  INFO: "text-sky-400",
  WARN: "text-amber-400",
  ERROR: "text-red-400",
};

const inputClass =
  "w-full rounded-lg border border-slate-700 bg-slate-800 px-3 py-2 text-sm text-white focus:border-purple-500 focus:outline-none focus:ring-1 focus:ring-purple-500";

function formatTime(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleTimeString();
}

export default function LogsSettings() {
  const {
    register,
    watch,
    formState: { errors },
  } = useFormContext<SettingsFormValues>();

  // The viewer shows up to log_captured_max lines (default 2000 in config,
  // capped by the form to >= 50). Keep the visible window in sync so the
  // setting has a real effect on both the snapshot and the live stream.
  const capturedMax = watch("log_captured_max");
  const limit = capturedMax && capturedMax > 0 ? capturedMax : 500;

  const { entries, connected, isLoading, path } = useLogStream(limit);

  return (
    <div>
      <Card
        title="Live Logs"
        actions={
          <span
            className={`text-xs ${connected ? "text-emerald-400" : "text-slate-400"}`}
            title={connected ? "Connected to live log stream" : "Live stream disconnected"}
          >
            {connected ? "● Live" : "○ Paused"}
          </span>
        }
      >
        <p className="mb-2 font-mono text-xs text-slate-500" title="On-disk log file (rotated)">
          {path}
        </p>
        {isLoading ? (
          <div className="flex justify-center py-8">
            <Spinner size="md" />
          </div>
        ) : (
          <div className="max-h-96 overflow-y-auto rounded-lg border border-slate-800 bg-black/40 p-3 font-mono text-xs leading-relaxed">
            {entries.length === 0 ? (
              <p className="text-slate-600">No captured log lines yet.</p>
            ) : (
              entries.map((e) => (
                <div key={logEntryKey(e)} className="flex gap-2 py-0.5">
                  <span className="shrink-0 text-slate-600">{formatTime(e.time)}</span>
                  <span
                    className={`w-14 shrink-0 font-bold ${LEVEL_COLORS[e.level] ?? "text-slate-400"}`}
                  >
                    {e.level}
                  </span>
                  <span className="break-all text-slate-200">{e.raw}</span>
                </div>
              ))
            )}
          </div>
        )}
      </Card>

      <Card title="Logging">
        <FormGroup
          label="Log Level"
          htmlFor="log_level"
          hint="Logging verbosity. Debug is most verbose. Changes apply immediately."
          error={errors.log_level?.message}
        >
          <select id="log_level" {...register("log_level")} className={inputClass}>
            <option value="debug">Debug</option>
            <option value="info">Info</option>
            <option value="warn">Warn</option>
            <option value="error">Error</option>
          </select>
        </FormGroup>

        <FormGroup
          label="Log Format"
          htmlFor="log_format"
          hint="Serialization of the log file and stderr (docker logs). JSON is machine-readable. Takes effect on restart."
          error={errors.log_format?.message}
        >
          <select id="log_format" {...register("log_format")} className={inputClass}>
            <option value="text">Text</option>
            <option value="json">JSON</option>
          </select>
        </FormGroup>

        <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
          <FormGroup
            label="Max Size (MB)"
            htmlFor="log_max_size_mb"
            hint="Rotate the log file when it reaches this size."
            error={errors.log_max_size_mb?.message}
          >
            <input
              id="log_max_size_mb"
              type="number"
              min={1}
              {...register("log_max_size_mb")}
              className={inputClass}
            />
          </FormGroup>

          <FormGroup
            label="Max Backups"
            htmlFor="log_max_backups"
            hint="Number of rotated log files to keep. Default 3."
            error={errors.log_max_backups?.message}
          >
            <input
              id="log_max_backups"
              type="number"
              min={0}
              {...register("log_max_backups")}
              className={inputClass}
            />
          </FormGroup>

          <FormGroup
            label="Max Age (Days)"
            htmlFor="log_max_age_days"
            hint="Oldest rotated logs to keep, in days. Default 7."
            error={errors.log_max_age_days?.message}
          >
            <input
              id="log_max_age_days"
              type="number"
              min={0}
              {...register("log_max_age_days")}
              className={inputClass}
            />
          </FormGroup>
        </div>

        <FormGroup
          label="Compress Rotated Logs"
          htmlFor="log_compress"
          hint="Gzip rotated log files to save disk space."
          error={errors.log_compress?.message}
        >
          <label className="flex items-center gap-2">
            <input id="log_compress" type="checkbox" {...register("log_compress")} className="h-4 w-4 rounded border-slate-700 bg-slate-800 text-purple-500 focus:ring-purple-500" />
            <span className="text-sm text-slate-300">Enable gzip compression</span>
          </label>
        </FormGroup>

        <FormGroup
          label="Access Log"
          htmlFor="log_access_log"
          hint="Write one line per request to access.log (nginx-style), separate from the app log. Polling endpoints are always excluded. Takes effect on restart."
          error={errors.log_access_log?.message}
        >
          <label className="flex items-center gap-2">
            <input id="log_access_log" type="checkbox" {...register("log_access_log")} className="h-4 w-4 rounded border-slate-700 bg-slate-800 text-purple-500 focus:ring-purple-500" />
            <span className="text-sm text-slate-300">Enable separate access log</span>
          </label>
        </FormGroup>

        <FormGroup
          label="Captured Log Lines"
          htmlFor="log_captured_max"
          hint="How many recent log lines the viewer reads from the log file."
          error={errors.log_captured_max?.message}
        >
          <input
            id="log_captured_max"
            type="number"
            min={50}
            {...register("log_captured_max")}
            className={inputClass}
          />
        </FormGroup>
      </Card>
    </div>
  );
}