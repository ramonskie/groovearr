import { sourceBadge } from "../settings/settings-schema";
import Badge from "../../components/Badge";

interface Props {
  capabilities?: Record<string, string>;
}

const CAP_LABELS: Record<string, string> = {
  download: "Download",
  metadata: "Metadata",
  playlist: "Playlists",
  discovery: "Discovery",
};

export default function CapabilityBadges({ capabilities }: Props) {
  if (!capabilities || Object.keys(capabilities).length === 0) return null;

  return (
    <div className="flex flex-wrap gap-1.5">
      {Object.entries(capabilities).map(([cap, status]) => {
        const badge = sourceBadge(status);
        const label = CAP_LABELS[cap] || cap;
        return (
          <Badge key={cap} variant={badge.variant}>
            {label}
          </Badge>
        );
      })}
    </div>
  );
}
