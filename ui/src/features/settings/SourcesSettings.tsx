import { useState } from "react";
import { useSources } from "../../hooks/use-config";
import Spinner from "../../components/Spinner";
import SubTabs from "../../components/SubTabs";
import SoulseekSection from "./SoulseekSection";
import DeezerSection from "./DeezerSection";
import MusicBrainzSection from "./MusicBrainzSection";
import SpotifySection from "./SpotifySection";
import MetadataOrderSection from "./MetadataOrderSection";
import DownloadOrderSection from "./DownloadOrderSection";

const TABS = [
  { id: "providers", label: "Providers" },
  { id: "priority", label: "Priority" },
] as const;

type TabId = (typeof TABS)[number]["id"];

export default function SourcesSettings() {
  const { data: sources, isLoading } = useSources();
  const [tab, setTab] = useState<TabId>("providers");

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

  return (
    <div>
      <SubTabs
        tabs={TABS.map((t) => ({ id: t.id, label: t.label }))}
        activeTab={tab}
        onTabChange={(id) => setTab(id as TabId)}
        className="mb-4"
      />

      {tab === "providers" && (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          <SoulseekSection source={soulseekSource} />
          <DeezerSection source={deezerSource} />
          <MusicBrainzSection source={mbSource} />
          <SpotifySection source={spotifySource} />
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
