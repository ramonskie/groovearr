import type { FC, FormEvent } from "react";
import { Search } from "lucide-react";
import Button from "../../components/Button";
import FormGroup from "../../components/FormGroup";

interface SearchFormProps {
  query: string;
  onQueryChange: (v: string) => void;
  source: string;
  onSourceChange: (v: string) => void;
  loading: boolean;
  onSubmit: () => void;
}

const SOURCE_OPTIONS: { value: string; label: string }[] = [
  { value: "", label: "Any configured" },
  { value: "soulseek", label: "Soulseek" },
  { value: "deezer", label: "Deezer" },
  { value: "hybrid", label: "Hybrid" },
];

const SearchForm: FC<SearchFormProps> = ({
  query,
  onQueryChange,
  source,
  onSourceChange,
  loading,
  onSubmit,
}) => {
  const handleSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (!query.trim()) return;
    onSubmit();
  };

  return (
    <form onSubmit={handleSubmit} className="flex flex-col gap-4 sm:flex-row sm:items-end">
      <FormGroup label="Search" className="flex-1">
        <div className="relative">
          <Search
            size={18}
            className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-slate-500"
          />
          <input
            id="search-query"
            type="text"
            value={query}
            onChange={(e) => onQueryChange(e.target.value)}
            placeholder="Artist, album, or track name..."
            autoFocus
            className="w-full rounded-lg border border-slate-700 bg-slate-900 py-2.5 pl-10 pr-3 text-sm text-white placeholder-slate-500 outline-none transition-colors focus:border-purple-500 focus:ring-1 focus:ring-purple-500"
          />
        </div>
      </FormGroup>

      <FormGroup label="Source" className="w-full sm:w-44">
        <select
          id="search-source"
          value={source}
          onChange={(e) => onSourceChange(e.target.value)}
          className="w-full rounded-lg border border-slate-700 bg-slate-900 py-2.5 px-3 text-sm text-white outline-none transition-colors focus:border-purple-500 focus:ring-1 focus:ring-purple-500"
        >
          {SOURCE_OPTIONS.map((opt) => (
            <option key={opt.value} value={opt.value}>
              {opt.label}
            </option>
          ))}
        </select>
      </FormGroup>

      <div className="self-end">
        <Button type="submit" loading={loading} disabled={!query.trim()}>
          Search
        </Button>
      </div>
    </form>
  );
};

export default SearchForm;
