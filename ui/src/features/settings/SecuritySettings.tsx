import { useFormContext } from "react-hook-form";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import Card from "../../components/Card";
import FormGroup from "../../components/FormGroup";
import Badge from "../../components/Badge";
import StatusMessage from "../../components/StatusMessage";
import { listUsers } from "../../api/client";
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

  // Login is backed by the user store, not by the fields on this page. Reading
  // the account list lets this card tell the truth about what switching to
  // Forms will do. Shares the ["users"] cache with the Users tab.
  const {
    data: users,
    isLoading: usersLoading,
    isError: usersError,
  } = useQuery({ queryKey: ["users"], queryFn: listUsers });
  const accounts = users ?? [];
  // Don't assert "no accounts" from a loading/errored query — that would show
  // false guidance while the admin-only list is still in flight.
  const accountsKnown = !usersLoading && !usersError;
  const hasAccounts = accounts.length > 0;
  const activeAdmins = accounts.filter(
    (u) => u.role === "admin" && !u.disabled,
  ).length;
  const hasActiveAdmin = activeAdmins > 0;

  return (
    <div>
      <Card title="Authentication">
        <FormGroup
          label="Authentication Method"
          htmlFor="auth_method"
          hint="None: no login required (requests are treated as admin). Forms: username + password login, backed by the accounts in the Users tab."
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
          <div className="mb-4">
            {!accountsKnown && (
              <StatusMessage variant="info">
                Enable Forms only when an enabled admin account exists. Manage
                accounts in the{" "}
                <Link to="/settings?tab=users" className="underline">
                  Users tab
                </Link>
                .
              </StatusMessage>
            )}
            {accountsKnown && !hasAccounts && (
              <StatusMessage variant="info">
                No accounts yet. Saving Forms will create the first admin from
                the username and password below (both required).
              </StatusMessage>
            )}
            {accountsKnown && hasAccounts && !hasActiveAdmin && (
              <StatusMessage variant="error">
                <span className="font-medium">No enabled admin account.</span>{" "}
                Forms login needs at least one active admin, otherwise nobody
                can reach these settings.{" "}
                <Link to="/settings?tab=users" className="underline">
                  Create or enable an admin
                </Link>{" "}
                in the Users tab before switching.
              </StatusMessage>
            )}
            {accountsKnown && hasAccounts && hasActiveAdmin && (
              <StatusMessage variant="info">
                {accounts.length} account{accounts.length === 1 ? "" : "s"} (
                {activeAdmins} admin{activeAdmins === 1 ? "" : "s"}). Sign in
                with one of them — the username and password below only create
                the first admin, which already exists.{" "}
                <Link to="/settings?tab=users" className="underline">
                  Manage users
                </Link>
                .
              </StatusMessage>
            )}
          </div>
        )}

        {authMethod === "forms" && (
          <>
            <FormGroup
              label="First admin username"
              htmlFor="auth_username"
              hint="Required whenever Forms is enabled. It creates the first admin only when no accounts exist; sign-in otherwise uses the Users list."
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
              label="First admin password"
              htmlFor="auth_password"
              hint="Required whenever Forms is enabled (8 characters minimum, 72 bytes maximum). Sets the credential used only to create the first admin; leave empty to keep the current value."
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
