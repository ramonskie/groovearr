import { useCallback, useRef, useState } from "react";
import { useFormContext } from "react-hook-form";
import { useSources } from "../../hooks/use-config";
import type { SettingsFormValues } from "./settings-schema";
import type { SourceInfo } from "../../api/types";

/** Returns true if the source provides metadata capability. */
function hasMetadata(s: SourceInfo): boolean {
  return s.capabilities != null && "metadata" in s.capabilities;
}

interface DragState {
  index: number;
}

export default function MetadataOrderSection() {
  const { watch, setValue } = useFormContext<SettingsFormValues>();
  const { data: sources } = useSources();

  const metadataOrder = watch("metadata_order") ?? [];
  const [dragOver, setDragOver] = useState<number | null>(null);
  const dragItem = useRef<DragState | null>(null);

  // Build ordered list: metadata-capable providers in metadata_order first,
  // then any configured metadata providers not yet in the order list.
  const configured = (sources ?? [])
    .filter((s) => hasMetadata(s) && s.configured)
    .map((s) => s.name);

  const ordered = [...new Set([...metadataOrder, ...configured])].filter((name) =>
    (sources ?? []).some((s) => s.name === name && hasMetadata(s)),
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
      setValue("metadata_order", next, { shouldDirty: true });
    },
    [ordered, setValue],
  );

  const handleDragEnd = useCallback(() => {
    dragItem.current = null;
    setDragOver(null);
  }, []);

  if (ordered.length === 0) return null;

  // Build display name lookup from sources.
  const displayNames: Record<string, string> = {};
  for (const s of sources ?? []) {
    displayNames[s.name] = s.display_name;
  }

  return (
    <div className="rounded-lg border border-slate-800 bg-slate-900/50 p-4">
      <div className="mb-3 flex items-center justify-between">
        <div>
          <h3 className="text-sm font-semibold text-white">Metadata Provider Order</h3>
          <p className="mt-0.5 text-xs text-slate-400">
            Drag to set priority. First provider is tried first when enriching metadata.
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
            <span>{displayNames[name] ?? name}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}
