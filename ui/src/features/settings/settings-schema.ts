import { z } from "zod";

// ─── Form schema (flat — matches UI fields) ────────────────────────

export const settingsFormSchema = z.object({
  // General
  download_path: z.string().optional(),

  // Soulseek
  slskd_url: z.preprocess(
    (v) => (v === "" ? undefined : v),
    z
      .string()
      .url("Must be a valid URL starting with http:// or https://")
      .optional(),
  ),
  slskd_api_key: z.string().optional(),

  // Deezer
  deezer_arl: z.string().optional(),
  deezer_quality: z.enum(["flac", "mp3_320", "mp3_128"]).optional(),

  // MusicBrainz
  musicbrainz_email: z.string().optional(),

  // Spotify
  spotify_mode: z.enum(["free", "dev"]).optional(),
  spotify_client_id: z.string().optional(),
  spotify_client_secret: z.string().optional(),
  spotify_redirect_uri: z.string().optional(),

  // Library
  library_path: z.string().optional(),
  folder_template: z.string().optional(),
  playlist_path: z.string().optional(),
  playlist_template: z.string().optional(),
});

export type SettingsFormValues = z.infer<typeof settingsFormSchema>;

// ─── Default values (used by useForm across sections) ───────────────

export const settingsDefaults: SettingsFormValues = {
  download_path: "",
  slskd_url: "",
  slskd_api_key: "",
  deezer_arl: "",
  deezer_quality: "flac",
  musicbrainz_email: "",
  spotify_mode: "free",
  spotify_client_id: "",
  spotify_client_secret: "",
  spotify_redirect_uri: "",
  library_path: "",
  folder_template: "",
  playlist_path: "",
  playlist_template: "",
};

// ─── Source badge variant mapping ───────────────────────────────────

export type BadgeVariant = "success" | "warning" | "muted";

export function sourceBadge(
  status: string,
): { variant: BadgeVariant; label: string } {
  switch (status) {
    case "connected":
      return { variant: "success", label: "Connected" };
    case "configured":
      return { variant: "warning", label: "Configured" };
    default:
      return { variant: "muted", label: "Not configured" };
  }
}
