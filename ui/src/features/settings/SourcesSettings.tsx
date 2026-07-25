import { useSources } from "../../hooks/use-config";
import { useSearchParams } from "react-router-dom";
import Spinner from "../../components/Spinner";
import SubTabs from "../../components/SubTabs";
import SoulseekSection from "./SoulseekSection";
import DeezerSection from "./DeezerSection";
import MusicBrainzSection from "./MusicBrainzSection";
import SpotifySection from "./SpotifySection";
import DiscogsSection from "./DiscogsSection";
import LastFmSection from "./LastFmSection";
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

  const soulseekSource = sources?.find((s) => s.name === "soulseek");
  const deezerSource = sources?.find((s) => s.name === "deezer");
  const mbSource = sources?.find((s) => s.name === "musicbrainz");
  const spotifySource = sources?.find((s) => s.name === "spotify");
  const discogsSource = sources?.find((s) => s.name === "discogs");
  const lastfmSource = sources?.find((s) => s.name === "lastfm");

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
          <SoulseekSection source={soulseekSource} />
          <DeezerSection source={deezerSource} />
          <MusicBrainzSection source={mbSource} />
          <SpotifySection source={spotifySource} />
          <DiscogsSection source={discogsSource} />
          <LastFmSection source={lastfmSource} />
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
