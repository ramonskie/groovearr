import { Globe, Music2, Disc3, Disc, Radio, Database, Image } from "lucide-react";
import type { LucideIcon } from "lucide-react";

const iconMap: Record<string, LucideIcon> = {
  globe: Globe,
  music2: Music2,
  disc3: Disc3,
  disc: Disc,
  radio: Radio,
  database: Database,
  image: Image,
};

export function getProviderIcon(iconId: string | undefined): LucideIcon {
  if (!iconId) return Globe;
  return iconMap[iconId] ?? Globe;
}
