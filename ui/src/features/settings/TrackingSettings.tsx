import { useFormContext } from "react-hook-form";
import Card from "../../components/Card";
import FormGroup from "../../components/FormGroup";
import type { SettingsFormValues } from "./settings-schema";

const inputClass =
  "w-full rounded-lg border border-slate-700 bg-slate-800 px-3 py-2 text-sm text-white placeholder:text-slate-500 focus:border-purple-500 focus:outline-none focus:ring-1 focus:ring-purple-500";

const checkboxClass =
  "h-4 w-4 rounded border-slate-700 bg-slate-800 text-purple-500 focus:ring-purple-500";

// Mirrors internal/config/config.go: empty/0 disables the loop, 1-4 is
// rejected (the scheduler only runs at >= 5 minutes), and anything above one
// year is treated as a mis-keyed value.
const REFRESH_MAX_MINUTES = 525600;

export default function TrackingSettings() {
  const {
    register,
    formState: { errors },
  } = useFormContext<SettingsFormValues>();

  return (
    <div>
      <Card title="Artist Tracking">
        <FormGroup
          label="Auto-search missing albums"
          htmlFor="tracking_auto_search_missing"
          hint="When enabled, the periodic refresh queues monitored wanted albums automatically."
        >
          <label className="flex items-center gap-2">
            <input
              id="tracking_auto_search_missing"
              type="checkbox"
              {...register("tracking_auto_search_missing")}
              className={checkboxClass}
            />
            <span className="text-sm text-slate-300">
              Search missing albums after each refresh
            </span>
          </label>
        </FormGroup>

        <FormGroup
          label="Refresh interval (minutes)"
          htmlFor="tracking_refresh_mins"
          hint="How often to check tracked artists for new releases. Leave empty or 0 to disable. Default 720 (12 hours). Takes effect after a restart — the scheduler reads it at startup."
          error={errors.tracking_refresh_mins?.message}
        >
          <input
            id="tracking_refresh_mins"
            type="number"
            min={0}
            max={REFRESH_MAX_MINUTES}
            placeholder="720"
            {...register("tracking_refresh_mins")}
            className={inputClass}
          />
        </FormGroup>

        <p className="mt-2 text-xs text-slate-500">
          Want a one-off search when adding an artist? Use the “Search missing
          albums on add” checkbox on the Discover page — that is the per-add{" "}
          <code className="rounded bg-slate-800 px-1 py-0.5 text-xs text-purple-400">
            search_on_add
          </code>{" "}
          trigger.
        </p>
      </Card>
    </div>
  );
}
