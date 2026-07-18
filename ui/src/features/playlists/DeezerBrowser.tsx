import { useMemo, type FC } from "react";
import { RefreshCw } from "lucide-react";
import { toast } from "sonner";
import type { Playlist, SourcePlaylistItem } from "../../api/types";
import {
  useBrowsePlaylistSource,
  useImportPlaylist,
  useSyncPlaylist,
} from "../../hooks/use-playlists";
import Card from "../../components/Card";
import DataTable, { type ColumnDef } from "../../components/DataTable";
import Button from "../../components/Button";
import Badge from "../../components/Badge";
import Spinner from "../../components/Spinner";

interface DeezerBrowserProps {
  isOpen: boolean;
  /** Already-imported playlists — used to resolve numeric IDs for Sync. */
  importedPlaylists: Playlist[];
}

const DeezerBrowser: FC<DeezerBrowserProps> = ({ isOpen, importedPlaylists }) => {
  const { data, isLoading, error } = useBrowsePlaylistSource(
    isOpen ? "deezer" : "",
  );
  const importMutation = useImportPlaylist();
  const syncMutation = useSyncPlaylist();

  // Build a lookup: source_playlist_id → numeric id
  const importedById = useMemo(() => {
    const map = new Map<string, number>();
    for (const p of importedPlaylists) {
      map.set(p.source_playlist_id, p.id);
    }
    return map;
  }, [importedPlaylists]);

  if (!isOpen) return null;

  type Row = SourcePlaylistItem & Record<string, unknown>;

  const columns: ColumnDef<Row>[] = [
    { key: "name", header: "Name" },
    { key: "track_count", header: "Tracks" },
    {
      key: "imported",
      header: "Status",
      render: (_val, row) => {
        const numericId = importedById.get(row.source_id);
        if (numericId !== undefined) {
          return (
            <Button
              variant="ghost"
              size="sm"
              loading={syncMutation.isPending && syncMutation.variables === numericId}
              onClick={() => {
                syncMutation.mutate(numericId, {
                  onSuccess: () =>
                    toast.success(`Syncing "${row.name}"`),
                  onError: (err) =>
                    toast.error(
                      err instanceof Error ? err.message : "Sync failed",
                    ),
                });
              }}
              title="Sync playlist"
            >
              <RefreshCw size={14} className="mr-1" />
              Sync
            </Button>
          );
        }
        return (
          <Button
            variant="primary"
            size="sm"
            loading={
              importMutation.isPending &&
              importMutation.variables?.playlist_id === row.source_id
            }
            onClick={() => {
              importMutation.mutate(
                { source: "deezer", playlist_id: row.source_id },
                {
                  onSuccess: (data) =>
                    toast.success(`Imported "${data.playlist.name}"`),
                  onError: (err) =>
                    toast.error(
                      err instanceof Error ? err.message : "Import failed",
                    ),
                },
              );
            }}
          >
            Import
          </Button>
        );
      },
    },
  ];

  return (
    <Card title="Browse Deezer Playlists" className="mt-4">
      {isLoading ? (
        <div className="flex justify-center py-4">
          <Spinner />
        </div>
      ) : error ? (
        <p className="text-sm text-red-400">
          {error instanceof Error
            ? error.message
            : "Failed to load Deezer playlists"}
        </p>
      ) : data && data.length > 0 ? (
        <DataTable
          columns={columns}
          data={data as Row[]}
          emptyMessage="No Deezer playlists found."
        />
      ) : (
        <p className="py-4 text-center text-sm text-slate-500">
          No Deezer playlists found.
        </p>
      )}
    </Card>
  );
};

export default DeezerBrowser;
