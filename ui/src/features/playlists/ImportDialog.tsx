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

/** Extract Deezer playlist numeric ID from a URL or raw ID string. */
function extractDeezerId(input: string): string | null {
  const trimmed = input.trim();
  if (!trimmed) return null;
  // e.g. https://www.deezer.com/us/playlist/123456789/
  const match = trimmed.match(/\/playlist\/(\d+)\//);
  if (match) return match[1];
  // Raw numeric ID
  if (/^\d+$/.test(trimmed)) return trimmed;
  return null;
}

const ImportDialog: FC<ImportDialogProps> = ({ open, onOpenChange }) => {
  const [input, setInput] = useState("");
  const importMutation = useImportPlaylist();

  const handleImport = () => {
    const playlistId = extractDeezerId(input);
    if (!playlistId) {
      toast.error("Invalid Deezer playlist URL or ID");
      return;
    }
    importMutation.mutate(
      { source: "deezer", playlist_id: playlistId },
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
              Import Deezer Playlist
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
              htmlFor="deezer-import-input"
              className="mb-1.5 block text-sm text-slate-400"
            >
              Playlist URL or numeric ID
            </label>
            <input
              id="deezer-import-input"
              type="text"
              value={input}
              onChange={(e) => setInput(e.target.value)}
              placeholder="https://www.deezer.com/.../playlist/123456/ or 123456"
              className="w-full rounded-lg border border-slate-700 bg-slate-800 px-3 py-2 text-sm text-white placeholder:text-slate-500 focus:border-purple-500 focus:outline-none"
              onKeyDown={(e) => {
                if (e.key === "Enter") handleImport();
              }}
            />
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
