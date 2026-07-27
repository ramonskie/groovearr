import { useState, type FC, useMemo } from "react";
import * as Dialog from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import { toast } from "sonner";
import { useImportPlaylist } from "../../hooks/use-playlists";
import { useSources } from "../../hooks/use-config";
import type { ImportPattern } from "../../api/types";
import Button from "../../components/Button";

type SyncMode = "mirror" | "append";

interface ImportDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

interface ParsedPlaylist {
  source: string;
  id: string;
}

/** Collect import URL patterns from all sources. */
function collectPatterns(sources: { name: string; display_name: string; ui_slots?: { import_url_patterns?: ImportPattern[] } }[]) {
  const result: { source: string; displayName: string; pattern: RegExp; isFallback: boolean }[] = [];
  for (const s of sources) {
    for (const p of s.ui_slots?.import_url_patterns ?? []) {
      result.push({
        source: s.name,
        displayName: s.display_name,
        pattern: new RegExp(p.pattern, "i"),
        isFallback: p.is_fallback ?? false,
      });
    }
  }
  // Sort: non-fallback first, then fallbacks
  result.sort((a, b) => (a.isFallback ? 1 : 0) - (b.isFallback ? 1 : 0));
  return result;
}

/** Parse a playlist URL or ID using collected patterns. */
function parsePlaylistURL(input: string, patterns: ReturnType<typeof collectPatterns>): ParsedPlaylist | null {
  const trimmed = input.trim();
  if (!trimmed) return null;

  for (const { source, pattern } of patterns) {
    const match = trimmed.match(pattern);
    if (match) {
      return { source, id: match[1] };
    }
  }

  return null;
}

const ImportDialog: FC<ImportDialogProps> = ({ open, onOpenChange }) => {
  const [input, setInput] = useState("");
  const [syncMode, setSyncMode] = useState<SyncMode>("mirror");
  const importMutation = useImportPlaylist();
  const { data: sources } = useSources();

  const patterns = useMemo(() => collectPatterns(sources ?? []), [sources]);

  const sourceLabels = useMemo(() => {
    const names = [...new Set(patterns.map((p) => p.displayName))];
    if (names.length === 0) return "a supported source";
    if (names.length === 1) return names[0];
    return names.slice(0, -1).join(", ") + " or " + names[names.length - 1];
  }, [patterns]);

  const handleImport = () => {
    const parsed = parsePlaylistURL(input, patterns);
    if (!parsed) {
      toast.error(`Invalid playlist URL — expected a playlist link from ${sourceLabels}`);
      return;
    }
    importMutation.mutate(
      { source: parsed.source, playlist_id: parsed.id, sync_mode: syncMode },
      {
        onSuccess: (data) => {
          toast.success(`Imported "${data.playlist.name}" with ${data.linked} linked tracks`);
          setInput("");
          onOpenChange(false);
        },
        onError: (err) => {
          toast.error(err instanceof Error ? err.message : "Import failed");
        },
      },
    );
  };

  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-black/60" />
        <Dialog.Content className="fixed left-1/2 top-1/2 z-50 w-full max-w-md -translate-x-1/2 -translate-y-1/2 rounded-lg border border-slate-800 bg-slate-900 p-6">
          <div className="mb-4 flex items-center justify-between">
            <Dialog.Title className="text-lg font-semibold text-white">
              Import Playlist
            </Dialog.Title>
            <Dialog.Close
              aria-label="Close"
              className="rounded p-1 text-slate-400 hover:bg-slate-800 hover:text-white"
            >
              <X size={18} />
            </Dialog.Close>
          </div>

          <div className="mb-4">
            <label
              htmlFor="playlist-import-input"
              className="mb-1.5 block text-sm text-slate-400"
            >
              Playlist URL
            </label>
            <input
              id="playlist-import-input"
              type="text"
              value={input}
              onChange={(e) => setInput(e.target.value)}
              placeholder={`Paste a ${sourceLabels} playlist URL`}
              className="w-full rounded-lg border border-slate-700 bg-slate-800 px-3 py-2 text-sm text-white placeholder:text-slate-500 focus:border-purple-500 focus:outline-none"
              onKeyDown={(e) => {
                if (e.key === "Enter") handleImport();
              }}
            />
            <p className="mt-1.5 text-xs text-slate-600">
              Paste a playlist URL from {sourceLabels}.
            </p>
          </div>

          <div className="mb-4">
            <label className="mb-1.5 block text-sm text-slate-400">
              Sync Mode
            </label>
            <div className="flex gap-2">
              {(["mirror", "append"] as SyncMode[]).map((mode) => (
                <label
                  key={mode}
                  className={`flex-1 cursor-pointer rounded-lg border px-3 py-2 text-center text-sm transition ${
                    syncMode === mode
                      ? "border-purple-500 bg-purple-500/10 text-purple-300"
                      : "border-slate-700 bg-slate-800 text-slate-400 hover:border-slate-600"
                  }`}
                >
                  <input
                    type="radio"
                    name="syncMode"
                    value={mode}
                    checked={syncMode === mode}
                    onChange={() => setSyncMode(mode)}
                    className="sr-only"
                  />
                  {mode === "mirror" ? "Mirror" : "Append"}
                </label>
              ))}
            </div>
            <p className="mt-1 text-xs text-slate-600">
              {syncMode === "mirror"
                ? "Remove local files when tracks are removed from the source playlist."
                : "Only add new tracks — never delete existing files."}
            </p>
          </div>

          <div className="flex justify-end gap-2">
            <Dialog.Close asChild>
              <Button variant="ghost" size="sm">
                Cancel
              </Button>
            </Dialog.Close>
            <Button
              variant="primary"
              size="sm"
              loading={importMutation.isPending}
              onClick={handleImport}
              disabled={!input.trim()}
            >
              Import
            </Button>
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
};

export default ImportDialog;
