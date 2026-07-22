import { useFormContext } from "react-hook-form";
import Card from "../../components/Card";
import FormGroup from "../../components/FormGroup";
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
  const apiKey = config?.auth?.api_key ?? "";

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
          htmlFor="auth_api_key"
          hint={authMethod === "forms"
            ? "Used for programmatic access. Also accepted via X-Api-Key header, ?apikey= query, or Authorization: Bearer."
            : "Not used while authentication is disabled. Will be required when you enable Forms or Basic auth."
          }
        >
          <div className="flex gap-2">
            <input
              id="auth_api_key"
              type="text"
              readOnly
              value={apiKey || (authMethod === "forms" ? "Generating..." : "Not active")}
              className="flex-1 rounded-lg border border-slate-700 bg-slate-800 px-3 py-2 text-sm font-mono text-slate-300 focus:outline-none"
            />
            <button
              type="button"
              onClick={() => {
                if (apiKey) navigator.clipboard.writeText(apiKey);
              }}
              disabled={!apiKey}
              className="rounded-lg border border-slate-700 bg-slate-800 px-3 py-2 text-sm text-slate-300 hover:bg-slate-700 transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
            >
              Copy
            </button>
          </div>
        </FormGroup>
      </Card>
    </div>
  );
}
