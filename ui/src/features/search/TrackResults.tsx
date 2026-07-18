import type { FC } from "react";
import type { TrackResult } from "../../api/types";
import DataTable from "../../components/DataTable";
import type { ColumnDef } from "../../components/DataTable";
import Badge from "../../components/Badge";

interface TrackResultsProps {
  tracks: TrackResult[];
  onDownload: (track: TrackResult) => void;
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

const TRACK_COLUMNS: ColumnDef<TrackResult>[] = [
  {
    key: "__index",
    header: "#",
    className: "w-12 text-slate-500",
    render: (_value, _row, index) => (
      <span className="tabular-nums">{index + 1}</span>
    ),
  },
  {
    key: "title",
    header: "Title",
    render: (_value, row) => (
      <div>
        <span className="text-sm font-medium text-white">
          {row.title || row.filename || "Unknown"}
        </span>
        {row.artist && (
          <span className="ml-2 text-xs text-slate-500">{row.artist}</span>
        )}
      </div>
    ),
  },
  {
    key: "quality",
    header: "Quality",
    className: "w-24",
    render: (_value, row) => (
      <Badge variant={qualityVariant(row.quality)}>{row.quality}</Badge>
    ),
  },
  {
    key: "size",
    header: "Size",
    className: "w-24 tabular-nums text-slate-400",
    render: (_value, row) => formatSize(row.size),
  },
  {
    key: "username",
    header: "Source",
    className: "w-32 text-slate-400",
    render: (_value, row) => row.username || "—",
  },
];

const TrackResults: FC<TrackResultsProps> = ({ tracks, onDownload }) => {
  const handleRowClick = (row: TrackResult) => {
    onDownload(row);
  };

  return (
    <DataTable<TrackResult>
      columns={TRACK_COLUMNS}
      data={tracks}
      onRowClick={handleRowClick}
      emptyMessage="No tracks found."
      getRowKey={(row, idx) => `${row.filename}-${idx}`}
    />
  );
};

export default TrackResults;
