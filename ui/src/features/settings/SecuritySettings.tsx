import { useFormContext } from "react-hook-form";
import Card from "../../components/Card";
import FormGroup from "../../components/FormGroup";
import Badge from "../../components/Badge";
import { useConfig } from "../../hooks/use-config";
import type { SettingsFormValues } from "./settings-schema";

export default function SecuritySettings() {
  const {
    register,
    watch,
    formState: { errors },
  } = useFormContext<SettingsFormValues>();

  const { data: config } = useConfig();
  const authMethod = watch("auth_method");
  // The raw key is never returned by any endpoint; `api_key` is the masked
  // sentinel ("********") and must not be rendered or copied. Render presence
  // from the derived `has_api_key` flag instead.
  const hasApiKey = config?.auth?.has_api_key ?? false;

  return (
    <div>
      <Card title="Authentication">
        <FormGroup
          label="Authentication Method"
          htmlFor="auth_method"
          hint="None: no authentication required. Forms: username + password login page."
          error={errors.auth_method?.message}
        >
          <select
            id="auth_method"
            {...register("auth_method")}
            className="w-full rounded-lg border border-slate-700 bg-slate-800 px-3 py-2 text-sm text-white focus:border-purple-500 focus:outline-none focus:ring-1 focus:ring-purple-500"
          >
            <option value="none">None</option>
            <option value="forms">Forms (Login Page)</option>
          </select>
        </FormGroup>

        {authMethod === "forms" && (
          <>
            <FormGroup
              label="Username"
              htmlFor="auth_username"
              hint="Login username for forms authentication."
              error={errors.auth_username?.message}
            >
              <input
                id="auth_username"
                type="text"
                autoComplete="username"
                placeholder="admin"
                {...register("auth_username")}
                className="w-full rounded-lg border border-slate-700 bg-slate-800 px-3 py-2 text-sm text-white placeholder:text-slate-500 focus:border-purple-500 focus:outline-none focus:ring-1 focus:ring-purple-500"
              />
            </FormGroup>

            <FormGroup
              label="Password"
              htmlFor="auth_password"
              hint="Set a new password (leave empty to keep current)."
              error={errors.auth_password?.message}
            >
              <input
                id="auth_password"
                type="password"
                autoComplete="new-password"
                placeholder="••••••••"
                {...register("auth_password")}
                className="w-full rounded-lg border border-slate-700 bg-slate-800 px-3 py-2 text-sm text-white placeholder:text-slate-500 focus:border-purple-500 focus:outline-none focus:ring-1 focus:ring-purple-500"
              />
            </FormGroup>
          </>
        )}
      </Card>

      <Card title="Local Network Bypass">
        <FormGroup
          label="Bypass Subnets"
          htmlFor="auth_local_bypass_subnets"
          hint="Devices in these subnets can access groovearr without authentication. Enter one CIDR range per line."
        >
          <textarea
            id="auth_local_bypass_subnets"
            rows={4}
            placeholder="192.168.1.0/24&#10;10.0.0.0/8"
            {...register("auth_local_bypass_subnets")}
            className="w-full rounded-lg border border-slate-700 bg-slate-800 px-3 py-2 text-sm text-white placeholder:text-slate-500 focus:border-purple-500 focus:outline-none focus:ring-1 focus:ring-purple-500 font-mono"
          />
        </FormGroup>
      </Card>

      <Card title="API Key">
        <FormGroup
          label="API Key"
          hint={
            authMethod === "forms"
              ? "Programmatic access credential. Sent via X-Api-Key header, ?apikey= query, or Authorization: Bearer. For security the raw key is never returned by the API, so it cannot be displayed or copied here."
              : "A key is generated automatically and accepted for programmatic access; it becomes the login credential once Forms is enabled. The raw key is never returned by the API."
          }
        >
          <div className="flex items-center gap-2">
            <Badge variant={hasApiKey ? "success" : "muted"}>
              {hasApiKey ? "Configured" : "Not set"}
            </Badge>
            <span className="text-xs text-slate-500">
              {hasApiKey
                ? "An API key is configured."
                : "No API key configured."}
            </span>
          </div>
        </FormGroup>
      </Card>
    </div>
  );
}
