import { useSources } from "../../hooks/use-config";
import Spinner from "../../components/Spinner";
import SoulseekSection from "./SoulseekSection";
import DeezerSection from "./DeezerSection";

export default function SourcesSettings() {
  const { data: sources, isLoading } = useSources();

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-8">
        <Spinner size="md" />
      </div>
    );
  }

  const soulseekSource = sources?.find((s) => s.name === "soulseek");
  const deezerSource = sources?.find((s) => s.name === "deezer");

  return (
    <div>
      <SoulseekSection source={soulseekSource} />
      <DeezerSection source={deezerSource} />
    </div>
  );
}
