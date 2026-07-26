import { useState, type FC } from "react";
import { Plus } from "lucide-react";
import { usePlaylists } from "../../hooks/use-playlists";
import { useSources } from "../../hooks/use-config";
import type { Playlist } from "../../api/types";
import { getProviderIcon } from "../settings/providerIcons";
import Button from "../../components/Button";
import Spinner from "../../components/Spinner";
import PlaylistCard from "./PlaylistCard";
import SourceBrowser from "./SourceBrowser";
import ImportDialog from "./ImportDialog";

const PlaylistsPage: FC = () => {
  const { data: playlists, isLoading } = usePlaylists();
  const { data: sources } = useSources();

  const [openBrowsers, setOpenBrowsers] = useState<Set<string>>(new Set());
  const [importOpen, setImportOpen] = useState(false);

  const list: Playlist[] = playlists ?? [];

  // Sources with playlist_browser capability that are connected for playlist
  const browserSources = (sources ?? [])
    .filter((s) => s.ui_slots?.playlist_browser && s.capabilities?.playlist === "connected")
    .sort((a, b) => a.display_name.localeCompare(b.display_name));

  const toggleBrowser = (name: string) => {
    setOpenBrowsers((prev) => {
      const next = new Set(prev);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      return next;
    });
  };

  return (
    <div>
      {/* ── Page header ── */}
      <div className="mb-6 flex flex-wrap items-center justify-between gap-3">
        <h2 className="text-xl font-bold text-white">Playlists</h2>
        <div className="flex items-center gap-2">
          {browserSources.map((s) => {
            const Icon = getProviderIcon(s.icon);
            const isOpen = openBrowsers.has(s.name);
            return (
              <Button key={s.name} variant="ghost" size="sm" onClick={() => toggleBrowser(s.name)}>
                <Icon size={14} className="mr-1" />
                {isOpen ? `Hide ${s.display_name}` : `Browse ${s.display_name}`}
              </Button>
            );
          })}
          <Button variant="primary" size="sm" onClick={() => setImportOpen(true)}>
            <Plus size={14} className="mr-1" />
            Import Playlist
          </Button>
        </div>
      </div>

      {/* ── Source browsers (togglable) ── */}
      {browserSources.map((s) => (
        <SourceBrowser
          key={s.name}
          sourceName={s.name}
          displayName={s.display_name}
          isOpen={openBrowsers.has(s.name)}
          importedPlaylists={list}
        />
      ))}

      {/* ── Imported playlists ── */}
      <div className="mt-4">
        {isLoading ? (
          <div className="flex justify-center py-12"><Spinner size="lg" /></div>
        ) : list.length === 0 ? (
          <p className="py-12 text-center text-sm text-slate-500">
            No playlists imported yet. Use Import to add one.
          </p>
        ) : (
          <div className="space-y-2">
            {list.map((p) => (
              <PlaylistCard key={p.id} playlist={p} />
            ))}
          </div>
        )}
      </div>

      {/* ── Import dialog ── */}
      <ImportDialog open={importOpen} onOpenChange={setImportOpen} />
    </div>
  );
};

export default PlaylistsPage;
