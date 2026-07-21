import { useState, type FC } from "react";
import * as Dialog from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import { toast } from "sonner";
import { useImportPlaylist } from "../../hooks/use-playlists";
import Button from "../../components/Button";

interface ImportDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

interface ParsedPlaylist {
  source: string;
  id: string;
}

const urlPatterns: { source: string; pattern: RegExp; extract: (m: RegExpMatchArray) => string }[] = [
  // Spotify: https://open.spotify.com/playlist/{id}?si=...
  {
    source: "spotify",
    pattern: /open\.spotify\.com\/playlist\/([a-zA-Z0-9]+)/,
    extract: (m) => m[1],
  },
  // Deezer: https://www.deezer.com/.../playlist/{id}/
  {
    source: "deezer",
    pattern: /\/playlist\/(\d+)\//,
    extract: (m) => m[1],
  },
];

/** Parse a playlist URL or ID into its source and identifier. */
function parsePlaylistURL(input: string): ParsedPlaylist | null {
  const trimmed = input.trim();
  if (!trimmed) return null;

  // Try known URL patterns first.
  for (const { source, pattern, extract } of urlPatterns) {
    const match = trimmed.match(pattern);
    if (match) {
      return { source, id: extract(match) };
    }
  }

  // Fallback: raw numeric ID → Deezer.
  if (/^\d+$/.test(trimmed)) {
    return { source: "deezer", id: trimmed };
  }

  return null;
}

const ImportDialog: FC<ImportDialogProps> = ({ open, onOpenChange }) => {
  const [input, setInput] = useState("");
  const importMutation = useImportPlaylist();

  const handleImport = () => {
    const parsed = parsePlaylistURL(input);
    if (!parsed) {
      toast.error("Invalid playlist URL — expected a Spotify or Deezer playlist link, or a numeric Deezer ID");
      return;
    }
    importMutation.mutate(
      { source: parsed.source, playlist_id: parsed.id },
      {
        onSuccess: (data) => {
          toast.success(
            `Imported "${data.playlist.name}" with ${data.linked} linked tracks`,
          );
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
              placeholder="https://open.spotify.com/playlist/... or https://www.deezer.com/.../playlist/.../"
              className="w-full rounded-lg border border-slate-700 bg-slate-800 px-3 py-2 text-sm text-white placeholder:text-slate-500 focus:border-purple-500 focus:outline-none"
              onKeyDown={(e) => {
                if (e.key === "Enter") handleImport();
              }}
            />
            <p className="mt-1.5 text-xs text-slate-600">
              Paste a Spotify or Deezer playlist URL. A numeric Deezer ID also works.
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
