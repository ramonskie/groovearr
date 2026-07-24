import { useCallback, useRef, useState } from "react";
import { useFormContext } from "react-hook-form";
import { useSources } from "../../hooks/use-config";
import type { SettingsFormValues } from "./settings-schema";

const DOWNLOAD_PROVIDERS = ["soulseek", "deezer"];

const PROVIDER_LABELS: Record<string, string> = {
  soulseek: "Soulseek",
  deezer: "Deezer",
};

interface DragState {
  index: number;
}

export default function DownloadOrderSection() {
  const { watch, setValue } = useFormContext<SettingsFormValues>();
  const { data: sources } = useSources();

  const downloadOrder = watch("download_order") ?? [];
  const [dragOver, setDragOver] = useState<number | null>(null);
  const dragItem = useRef<DragState | null>(null);

  // Only show connected download providers.
  const connected = (sources ?? [])
    .filter((s) => DOWNLOAD_PROVIDERS.includes(s.name) && s.status === "connected")
    .map((s) => s.name);

  const ordered = [...new Set([...downloadOrder, ...connected])].filter((name) =>
    DOWNLOAD_PROVIDERS.includes(name),
  );

  const handleDragStart = useCallback((index: number) => {
    dragItem.current = { index };
  }, []);

  const handleDragOver = useCallback(
    (e: React.DragEvent, index: number) => {
      e.preventDefault();
      setDragOver(index);
    },
    [],
  );

  const handleDragLeave = useCallback(() => {
    setDragOver(null);
  }, []);

  const handleDrop = useCallback(
    (e: React.DragEvent, dropIndex: number) => {
      e.preventDefault();
      setDragOver(null);
      if (dragItem.current === null) return;

      const fromIndex = dragItem.current.index;
      dragItem.current = null;
      if (fromIndex === dropIndex) return;

      const next = [...ordered];
      const [moved] = next.splice(fromIndex, 1);
      next.splice(dropIndex, 0, moved);
      setValue("download_order", next, { shouldDirty: true });
    },
    [ordered, setValue],
  );

  const handleDragEnd = useCallback(() => {
    dragItem.current = null;
    setDragOver(null);
  }, []);

  if (ordered.length === 0) return null;

  return (
    <div className="rounded-lg border border-slate-800 bg-slate-900/50 p-4">
      <div className="mb-3 flex items-center justify-between">
        <div>
          <h3 className="text-sm font-semibold text-white">Download Provider Order</h3>
          <p className="mt-0.5 text-xs text-slate-400">
            Drag to set priority. First provider is searched first. Only connected providers shown.
          </p>
        </div>
      </div>
      <ul className="space-y-1">
        {ordered.map((name, i) => (
          <li
            key={name}
            draggable
            onDragStart={() => handleDragStart(i)}
            onDragOver={(e) => handleDragOver(e, i)}
            onDragLeave={handleDragLeave}
            onDrop={(e) => handleDrop(e, i)}
            onDragEnd={handleDragEnd}
            className={`flex cursor-grab items-center gap-2 rounded-md border px-3 py-2 text-sm transition-colors active:cursor-grabbing ${
              dragOver === i
                ? "border-purple-500 bg-purple-950/30 text-purple-200"
                : "border-slate-700 bg-slate-800 text-slate-200 hover:border-slate-600"
            }`}
          >
            <span className="text-slate-500 tabular-nums">{i + 1}.</span>
            <span className="text-slate-400 select-none">⠿</span>
            <span>{PROVIDER_LABELS[name] ?? name}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}
