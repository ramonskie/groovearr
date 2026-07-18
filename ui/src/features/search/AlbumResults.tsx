import type { FC } from "react";
import type { AlbumResult } from "../../api/types";
import DataTable from "../../components/DataTable";
import type { ColumnDef } from "../../components/DataTable";
import Badge from "../../components/Badge";

interface AlbumResultsProps {
  albums: AlbumResult[];
  onDownload: (album: AlbumResult) => void;
}

function formatSize(bytes: number): string {
  if (bytes === 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  let size = bytes;
  while (size >= 1024 && i < units.length - 1) {
    size /= 1024;
    i++;
  }
  return `${size.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

function qualityVariant(quality: string): "success" | "warning" | "muted" {
  const q = quality.toLowerCase();
  if (q.includes("flac") || q.includes("lossless")) return "success";
  if (q.includes("320")) return "warning";
  return "muted";
}

const ALBUM_COLUMNS: ColumnDef<AlbumResult>[] = [
  {
    key: "__index",
    header: "#",
    className: "w-12 text-slate-500",
    render: (_value, _row, index) => (
      <span className="tabular-nums">{index + 1}</span>
    ),
  },
  {
    key: "album_title",
    header: "Album",
    render: (_value, row) => (
      <div>
        <span className="text-sm font-medium text-white">
          {row.album_title || "Unknown"}
        </span>
        {(row.artist || row.year) && (
          <span className="ml-2 text-xs text-slate-500">
            {[row.artist, row.year].filter(Boolean).join(" · ")}
          </span>
        )}
      </div>
    ),
  },
  {
    key: "track_count",
    header: "Tracks",
    className: "w-20 tabular-nums text-slate-400",
    render: (_value, row) => row.track_count,
  },
  {
    key: "dominant_quality",
    header: "Quality",
    className: "w-24",
    render: (_value, row) => (
      <Badge variant={qualityVariant(row.dominant_quality)}>
        {row.dominant_quality}
      </Badge>
    ),
  },
  {
    key: "total_size",
    header: "Size",
    className: "w-24 tabular-nums text-slate-400",
    render: (_value, row) => formatSize(row.total_size),
  },
];

const AlbumResults: FC<AlbumResultsProps> = ({ albums, onDownload }) => {
  const handleRowClick = (row: AlbumResult) => {
    onDownload(row);
  };

  return (
    <DataTable<AlbumResult>
      columns={ALBUM_COLUMNS}
      data={albums}
      onRowClick={handleRowClick}
      emptyMessage="No albums found."
      getRowKey={(row, idx) => `${row.album_path}-${idx}`}
    />
  );
};

export default AlbumResults;
