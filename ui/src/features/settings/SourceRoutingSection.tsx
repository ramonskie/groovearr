import { useFormContext } from "react-hook-form";
import Card from "../../components/Card";
import { useSources } from "../../hooks/use-config";
import type { SettingsFormValues } from "./settings-schema";
import type { SourceInfo } from "../../api/types";

function hasCap(s: SourceInfo, cap: string): boolean {
  return s.capabilities != null && cap in s.capabilities;
}

/** Toggles a name in a multi-select array kept in config order. */
function toggleIn(name: string, current: string[], setValue: (v: string[]) => void): void {
  const next = current.includes(name)
    ? current.filter((n) => n !== name)
    : [...current, name];
  setValue(next);
}

export default function SourceRoutingSection() {
  const { watch, setValue } = useFormContext<SettingsFormValues>();
  const { data: sources } = useSources();

  const albumSources = watch("album_sources") ?? [];
  const downloadClient = watch("download_client") ?? "";

  const all = sources ?? [];
  const albumOptions = all.filter((s) => hasCap(s, "album_search"));
  const clientOptions = all.filter((s) => hasCap(s, "download_client"));

  const checkbox =
    "h-4 w-4 rounded border-slate-700 bg-slate-800 text-purple-500 focus:ring-purple-500";

  return (
    <div className="space-y-4">
      {albumOptions.length > 0 && (
        <Card title="Album Sources">
          <p className="mb-3 text-xs text-slate-500">
            Sources used for full-album downloads (e.g. Prowlarr). Uncheck to disable.
          </p>
          <div className="space-y-2">
            {albumOptions.map((s) => {
              const selected = albumSources.includes(s.name);
              const disabled = s.enabled === false && !selected;
              return (
                <label
                  key={s.name}
                  className={
                    "flex items-center gap-3 " +
                    (disabled ? "cursor-not-allowed opacity-40" : "cursor-pointer")
                  }
                >
                  <input
                    type="checkbox"
                    checked={selected}
                    disabled={disabled}
                    onChange={(e) =>
                      toggleIn(s.name, albumSources, (v) =>
                        setValue("album_sources", v, { shouldDirty: true }),
                      )
                    }
                    className={checkbox}
                  />
                  <span className="text-sm text-white">{s.display_name}</span>
                  {s.enabled === false && (
                    <span className="text-xs text-slate-500">(disabled in Providers)</span>
                  )}
                </label>
              );
            })}
          </div>
        </Card>
      )}

      {clientOptions.length > 0 && (
        <Card title="Download Client">
          <p className="mb-3 text-xs text-slate-500">
            The client that executes album downloads (e.g. qBittorrent).
          </p>
          <div className="space-y-2">
            {clientOptions.map((s) => {
              const selected = downloadClient === s.name;
              const disabled = s.enabled === false && !selected;
              return (
                <label
                  key={s.name}
                  className={
                    "flex items-center gap-3 " +
                    (disabled ? "cursor-not-allowed opacity-40" : "cursor-pointer")
                  }
                >
                  <input
                    type="radio"
                    name="download_client"
                    checked={selected}
                    disabled={disabled}
                    onChange={() =>
                      setValue("download_client", s.name, { shouldDirty: true })
                    }
                    className="h-4 w-4 border-slate-700 bg-slate-800 text-purple-500 focus:ring-purple-500"
                  />
                  <span className="text-sm text-white">{s.display_name}</span>
                  {s.enabled === false && (
                    <span className="text-xs text-slate-500">(disabled in Providers)</span>
                  )}
                </label>
              );
            })}
          </div>
        </Card>
      )}
    </div>
  );
}
