import { useState, type FC } from "react";
import { Search, Plus } from "lucide-react";
import { usePlaylists } from "../../hooks/use-playlists";
import type { Playlist } from "../../api/types";
import Button from "../../components/Button";
import Spinner from "../../components/Spinner";
import PlaylistCard from "./PlaylistCard";
import DeezerBrowser from "./DeezerBrowser";
import ImportDialog from "./ImportDialog";

const PlaylistsPage: FC = () => {
  const { data: playlists, isLoading } = usePlaylists();

  const [showBrowser, setShowBrowser] = useState(false);
  const [importOpen, setImportOpen] = useState(false);

  const list: Playlist[] = playlists ?? [];

  return (
    <div>
      {/* ── Page header ── */}
      <div className="mb-6 flex flex-wrap items-center justify-between gap-3">
        <h2 className="text-xl font-bold text-white">Playlists</h2>
        <div className="flex items-center gap-2">
          <Button variant="ghost" size="sm" onClick={() => setShowBrowser((v) => !v)}>
            <Search size={14} className="mr-1" />
            {showBrowser ? "Hide Deezer" : "Browse Deezer"}
          </Button>
          <Button variant="primary" size="sm" onClick={() => setImportOpen(true)}>
            <Plus size={14} className="mr-1" />
            Import by ID
          </Button>
        </div>
      </div>

      {/* ── Deezer browser (togglable) ── */}
      <DeezerBrowser isOpen={showBrowser} importedPlaylists={list} />

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
