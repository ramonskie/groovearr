import { sourceBadge } from "../settings/settings-schema";
import Badge from "../../components/Badge";

interface Props {
  capabilities?: Record<string, string>;
  capabilityAccess?: Record<string, "public" | "account">;
}

const CAP_LABELS: Record<string, string> = {
  download: "Download",
  metadata: "Metadata",
  playlist: "Playlists",
  discovery: "Discovery",
};

export default function CapabilityBadges({ capabilities, capabilityAccess }: Props) {
  if (!capabilities || Object.keys(capabilities).length === 0) return null;

  return (
    <div className="flex flex-wrap gap-1.5">
      {Object.entries(capabilities).map(([cap, status]) => {
        const badge = sourceBadge(status);
        const label = CAP_LABELS[cap] || cap;
        const access = capabilityAccess?.[cap];
        const suffix =
          access === "public" ? (
            <span className="ml-1 font-normal opacity-70">· public</span>
          ) : null;
        return (
          <Badge key={cap} variant={badge.variant} title={
            access === "public"
              ? "Works via public API — no credentials required"
              : access === "account"
                ? "Requires the provider's configured credentials"
                : undefined
          }>
            {label}
            {suffix}
          </Badge>
        );
      })}
    </div>
  );
}
