import type { ReactNode, FC } from "react";

export interface ColumnDef<T> {
  key: string;
  header: string;
  className?: string;
  sortable?: boolean;
  render?: (value: unknown, row: T, index: number) => ReactNode;
}

type SortDirection = "asc" | "desc" | null;

interface SortState {
  key: string;
  direction: SortDirection;
}

interface DataTableProps<T> {
  columns: ColumnDef<T>[];
  data: T[];
  sortState?: SortState;
  onSort?: (key: string) => void;
  onRowClick?: (row: T) => void;
  emptyMessage?: string;
  className?: string;
  getRowKey?: (row: T, index: number) => string | number;
}

function DataTable<T extends Record<string, unknown>>({
  columns,
  data,
  sortState,
  onSort,
  onRowClick,
  emptyMessage = "No data available.",
  className = "",
  getRowKey,
}: DataTableProps<T>): ReturnType<FC> {
  const handleSort = (col: ColumnDef<T>) => {
    if (col.sortable && onSort) {
      onSort(col.key);
    }
  };

  const sortIndicator = (col: ColumnDef<T>) => {
    if (!sortState || sortState.key !== col.key) return null;
    return (
      <span className="ml-1 inline-block text-purple-400">
        {sortState.direction === "asc" ? "▲" : "▼"}
      </span>
    );
  };

  return (
    <div className={`overflow-x-auto ${className}`}>
      <table className="w-full text-left text-sm">
        <thead>
          <tr className="border-b border-slate-800">
            {columns.map((col) => (
              <th
                key={col.key}
                onClick={() => handleSort(col)}
                className={`px-4 py-3 text-xs font-semibold uppercase tracking-wider text-slate-400 ${
                  col.sortable ? "cursor-pointer select-none hover:text-slate-200" : ""
                } ${col.className ?? ""}`}
              >
                {col.header}
                {sortIndicator(col)}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {data.length === 0 ? (
            <tr>
              <td
                colSpan={columns.length}
                className="px-4 py-12 text-center text-slate-500"
              >
                {emptyMessage}
              </td>
            </tr>
          ) : (
            data.map((row, rowIdx) => {
              const rowKey =
                getRowKey?.(row, rowIdx) ?? (row.id as string) ?? rowIdx;
              return (
                <tr
                  key={rowKey}
                  onClick={() => onRowClick?.(row)}
                  className={`border-b border-slate-800/50 ${
                    onRowClick
                      ? "cursor-pointer hover:bg-slate-800/50"
                      : ""
                  }`}
                >
                  {columns.map((col) => {
                    const value = row[col.key];
                    return (
                      <td
                        key={col.key}
                        className={`px-4 py-3 text-slate-300 ${col.className ?? ""}`}
                      >
                        {col.render
                          ? col.render(value, row, rowIdx)
                          : (value as ReactNode) ?? "—"}
                      </td>
                    );
                  })}
                </tr>
              );
            })
          )}
        </tbody>
      </table>
    </div>
  );
}

export default DataTable;
