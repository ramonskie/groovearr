import { z } from "zod";
import type { SourceInfo } from "../../api/types";

// ─── Form schema — dynamic source fields + static app fields ───────

export function buildFormSchema(sources: SourceInfo[]) {
  const sourceSchemas: Record<string, z.ZodTypeAny> = {};
  for (const s of sources) {
    const fieldSchemas: Record<string, z.ZodTypeAny> = {};
    for (const field of s.config_schema ?? []) {
      let fieldSchema: z.ZodTypeAny = z.string().optional();
      if (field.required) {
        fieldSchema = z.string().min(1, `${field.label} is required`);
      }
      if (field.validation?.format === "url") {
        const urlSchema = z.string().url("Must be a valid URL starting with http:// or https://");
        if (field.required) {
          fieldSchema = urlSchema;
        } else {
          fieldSchema = z.preprocess(
            (v) => (v === "" ? undefined : v),
            urlSchema.optional(),
          );
        }
      }
      if (field.validation?.format === "email") {
        fieldSchema = z.string().email("Must be a valid email").optional().or(z.literal(""));
      }
      fieldSchemas[field.name] = fieldSchema;
    }
    sourceSchemas[s.name] = z.object(fieldSchemas).optional();
  }

  return z.object({
    // General
    download_path: z.string().optional(),

    // Dynamic sources
    sources: z.object(sourceSchemas).optional(),

    // Library
    library_path: z.string().optional(),
    folder_template: z.string().optional(),
    playlist_path: z.string().optional(),
    playlist_template: z.string().optional(),
    playlist_auto_sync_mins: z.preprocess(
      (v) => (v === "" || v === undefined ? undefined : Number(v)),
      z
        .number()
        .int()
        .min(0, "Must be 0 or greater")
        .refine((v) => v === 0 || v >= 5, "Must be 0 (disabled) or at least 5 minutes")
        .optional(),
    ),

    // Auth (Security)
    auth_method: z.enum(["none", "forms"]).optional(),
    auth_username: z.string().optional(),
    auth_password: z.string().min(4, "Password must be at least 4 characters").or(z.literal("")).optional(),
    auth_api_key: z.string().optional(),
    auth_local_bypass_subnets: z.string().optional(),

    // Metadata provider order
    metadata_order: z.array(z.string()).optional(),

    // Download provider order
    download_order: z.array(z.string()).optional(),

    // Track / album source routing + default download client
    album_sources: z.array(z.string()).optional(),
    download_client: z.string().optional(),

    // Logging (Logs)
    log_level: z.enum(["debug", "info", "warn", "error"]).optional(),
    log_format: z.enum(["json", "text"]).optional(),
    log_max_size_mb: z.preprocess(
      (v) => (v === "" || v === undefined ? undefined : Number(v)),
      z.number().int().min(1, "Must be at least 1 MB").optional(),
    ),
    log_max_backups: z.preprocess(
      (v) => (v === "" || v === undefined ? undefined : Number(v)),
      z.number().int().min(1, "Must be at least 1").optional(),
    ),
    log_max_age_days: z.preprocess(
      (v) => (v === "" || v === undefined ? undefined : Number(v)),
      z.number().int().min(1, "Must be at least 1").optional(),
    ),
    log_compress: z.boolean().optional(),
    log_captured_max: z.preprocess(
      (v) => (v === "" || v === undefined ? undefined : Number(v)),
      z.number().int().min(50, "Must be at least 50").optional(),
    ),
  });
}

export type SettingsFormValues = z.infer<ReturnType<typeof buildFormSchema>>;

// ─── Default values (used by useForm across sections) ───────────────

export function buildDefaults(sources: SourceInfo[]) {
  const sourceDefaults: Record<string, Record<string, string>> = {};
  for (const s of sources) {
    const fields: Record<string, string> = {};
    for (const field of s.config_schema ?? []) {
      fields[field.name] = field.default ?? "";
    }
    sourceDefaults[s.name] = fields;
  }

  return {
    download_path: "",
    sources: sourceDefaults,
    library_path: "",
    folder_template: "",
    playlist_path: "",
    playlist_template: "",
    playlist_auto_sync_mins: undefined as number | undefined,
    auth_method: "none" as const,
    auth_username: "",
    auth_password: "",
    auth_api_key: "",
    auth_local_bypass_subnets: "",
    metadata_order: [] as string[],
    download_order: [] as string[],
    album_sources: [] as string[],
    download_client: "",
    log_level: "info" as const,
    log_format: "json" as const,
    log_max_size_mb: 10,
    log_max_backups: 3,
    log_max_age_days: 7,
    log_compress: true,
    log_captured_max: 2000,
  };
}

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
