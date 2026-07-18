import { useSources } from "../../hooks/use-config";
import type { ConfigUpdatePayload } from "../../api/types";
import Spinner from "../../components/Spinner";
import SoulseekSection from "./SoulseekSection";
import DeezerSection from "./DeezerSection";

interface Props {
  onSave: (payload: ConfigUpdatePayload, section: string) => void;
  isSaving: boolean;
}

export default function SourcesSettings({ onSave, isSaving }: Props) {
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
      <SoulseekSection
        source={soulseekSource}
        onSave={onSave}
        isSaving={isSaving}
      />
      <DeezerSection
        source={deezerSource}
        onSave={onSave}
        isSaving={isSaving}
      />
    </div>
  );
}
