import { useSources } from "../../hooks/use-config";
import { useSearchParams } from "react-router-dom";
import Spinner from "../../components/Spinner";
import SubTabs from "../../components/SubTabs";
import ProviderSection from "./ProviderSection";
import MetadataOrderSection from "./MetadataOrderSection";
import DownloadOrderSection from "./DownloadOrderSection";

const TABS = [
  { id: "providers", label: "Providers" },
  { id: "priority", label: "Priority" },
] as const;

type TabId = (typeof TABS)[number]["id"];

export default function SourcesSettings() {
  const { data: sources, isLoading } = useSources();
  const [searchParams, setSearchParams] = useSearchParams();
  const tab: TabId = (() => {
    const fromParam = searchParams.get("sourceTab");
    if (fromParam && TABS.some((t) => t.id === fromParam)) return fromParam as TabId;
    return "providers";
  })();

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-8">
        <Spinner size="md" />
      </div>
    );
  }

  return (
    <div>
      <SubTabs
        tabs={TABS.map((t) => ({ id: t.id, label: t.label }))}
        activeTab={tab}
        onTabChange={(id) => {
          setSearchParams((prev) => {
            const next = new URLSearchParams(prev);
            next.set("sourceTab", id);
            return next;
          });
        }}
        className="mb-4"
      />

      {tab === "providers" && (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          {(sources ?? [])
            .filter((s) => (s.config_schema && s.config_schema.length > 0) || s.oauth?.enabled)
            .sort((a, b) => a.display_name.localeCompare(b.display_name))
            .map((source) => (
              <ProviderSection key={source.name} source={source} />
            ))}
        </div>
      )}

      {tab === "priority" && (
        <div className="space-y-4">
          <MetadataOrderSection />
          <DownloadOrderSection />
        </div>
      )}
    </div>
  );
}
