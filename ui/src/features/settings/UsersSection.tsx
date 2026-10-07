import { useMemo, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Eye, EyeOff, KeyRound } from "lucide-react";
import {
  createUser,
  deleteUser,
  listUsers,
  updateUser,
} from "../../api/client";
import type {
  CreateUserRequest,
  UpdateUserRequest,
  UserRecord,
  UserRole,
} from "../../api/types";
import Card from "../../components/Card";
import FormGroup from "../../components/FormGroup";
import Button from "../../components/Button";
import Badge from "../../components/Badge";
import StatusMessage from "../../components/StatusMessage";
import DataTable, { type ColumnDef } from "../../components/DataTable";
import Spinner from "../../components/Spinner";
import { useAuth } from "../../context/AuthContext";

// ─── Constants ────────────────────────────────────────────────────────

/**
 * Mirrors the backend boundary: `handlers_users.go` requires at least 8
 * characters, and bcrypt caps the hash input at 72 bytes
 * (`bcrypt.ErrPasswordTooLong`). The backend returns 400 below the minimum and
 * 500 from the failed hash above the maximum, so we guard both client-side.
 */
const MIN_PASSWORD_LENGTH = 8;
const MAX_PASSWORD_BYTES = 72;
const GENERATED_PASSWORD_LENGTH = 16;

/** UTF-8 byte length, matching Go's `len(string)` and bcrypt's byte cap. */
function passwordByteLength(value: string): number {
  return new TextEncoder().encode(value).length;
}
/** Look-alike characters (0/O, 1/l/I) intentionally omitted. */
const PASSWORD_ALPHABET =
  "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789!@#$%^&*";

const inputClass =
  "w-full rounded-lg border border-slate-700 bg-slate-800 px-3 py-2 text-sm text-white placeholder:text-slate-500 focus:border-purple-500 focus:outline-none focus:ring-1 focus:ring-purple-500";

const selectClass =
  "rounded-lg border border-slate-700 bg-slate-800 px-3 py-2 text-sm text-white focus:border-purple-500 focus:outline-none focus:ring-1 focus:ring-purple-500";

// UserRecord is an interface without an index signature; DataTable requires
// T extends Record<string, unknown>. Intersecting with the index signature
// keeps row access type-safe (same pattern as SourceBrowser's local Row).
type UserRow = UserRecord & Record<string, unknown>;

/** Cryptographically random temporary password for the "Generate" action. */
function generatePassword(): string {
  const bytes = new Uint32Array(GENERATED_PASSWORD_LENGTH);
  crypto.getRandomValues(bytes);
  return Array.from(
    bytes,
    (b) => PASSWORD_ALPHABET[b % PASSWORD_ALPHABET.length],
  ).join("");
}

function formatCreatedAt(value: string): string {
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString();
}

const ROLE_BADGE: Record<UserRole, "warning" | "muted"> = {
  admin: "warning",
  user: "muted",
};

/**
 * Admin-only Settings → Users section (plan Phase 8.4).
 *
 * Create + manage accounts. Guards are surfaced client-side (disabled controls
 * with a reason) and backstopped by the server:
 *   - the last active admin cannot be deleted, demoted, or disabled (409);
 *   - you cannot delete your own account (403).
 * The server is authoritative; a mutation error is shown via toast/inline.
 */
export default function UsersSection() {
  const queryClient = useQueryClient();
  const { username: currentUsername } = useAuth();

  const usersQuery = useQuery({
    queryKey: ["users"],
    queryFn: listUsers,
  });

  // ─── Create form (local state; independent of the settings RHF form) ──
  const [newUsername, setNewUsername] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [newRole, setNewRole] = useState<UserRole>("user");
  const [showPassword, setShowPassword] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);

  const createMutation = useMutation({
    mutationFn: (payload: CreateUserRequest) => createUser(payload),
    onSuccess: (created) => {
      toast.success(`Created user "${created.username}"`);
      setNewUsername("");
      setNewPassword("");
      setNewRole("user");
      setShowPassword(false);
      setCreateError(null);
      queryClient.invalidateQueries({ queryKey: ["users"] });
    },
    onError: (err) => {
      setCreateError(
        err instanceof Error ? err.message : "Failed to create user",
      );
    },
  });

  const updateMutation = useMutation({
    mutationFn: ({ id, payload }: { id: number; payload: UpdateUserRequest }) =>
      updateUser(id, payload),
    onSuccess: (updated) => {
      toast.success(`Updated "${updated.username}"`);
      queryClient.invalidateQueries({ queryKey: ["users"] });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Update failed");
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (id: number) => deleteUser(id),
    onSuccess: () => {
      toast.success("User deleted");
      queryClient.invalidateQueries({ queryKey: ["users"] });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Delete failed");
    },
  });

  const users = useMemo(() => usersQuery.data ?? [], [usersQuery.data]);

  // The last active admin is the only enabled admin left. Delete, demote, and
  // disable must all be blocked client-side (server backstops with 409).
  const activeAdminCount = useMemo(
    () => users.filter((u) => u.role === "admin" && !u.disabled).length,
    [users],
  );
  const isLastAdmin = (u: UserRecord) =>
    u.role === "admin" && !u.disabled && activeAdminCount <= 1;

  const selfName = (currentUsername ?? "").toLowerCase();
  const isSelf = (u: UserRecord) =>
    selfName !== "" && u.username.toLowerCase() === selfName;

  const busy = updateMutation.isPending || deleteMutation.isPending;
  const rowBusy = (id: number) =>
    (updateMutation.isPending && updateMutation.variables?.id === id) ||
    (deleteMutation.isPending && deleteMutation.variables === id);

  // ─── Create submit ──────────────────────────────────────────────────
  const handleCreate = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const username = newUsername.trim();
    if (username === "") {
      setCreateError("Username is required.");
      return;
    }
    if (newPassword.length < MIN_PASSWORD_LENGTH) {
      setCreateError(
        `Password must be at least ${MIN_PASSWORD_LENGTH} characters.`,
      );
      return;
    }
    if (passwordByteLength(newPassword) > MAX_PASSWORD_BYTES) {
      setCreateError(
        `Password must be at most ${MAX_PASSWORD_BYTES} bytes.`,
      );
      return;
    }
    setCreateError(null);
    createMutation.mutate({ username, password: newPassword, role: newRole });
  };

  // ─── Row actions ────────────────────────────────────────────────────
  const changeRole = (u: UserRecord, role: UserRole) => {
    if (role === u.role) return;
    updateMutation.mutate({ id: u.id, payload: { role } });
  };

  const toggleDisabled = (u: UserRecord) => {
    updateMutation.mutate({ id: u.id, payload: { disabled: !u.disabled } });
  };

  const resetPassword = (u: UserRecord) => {
    const next = window.prompt(
      `New password for "${u.username}" (min ${MIN_PASSWORD_LENGTH} characters, max ${MAX_PASSWORD_BYTES} bytes):`,
    );
    if (next === null) return; // cancelled
    if (next.length < MIN_PASSWORD_LENGTH) {
      toast.error(
        `Password must be at least ${MIN_PASSWORD_LENGTH} characters.`,
      );
      return;
    }
    if (passwordByteLength(next) > MAX_PASSWORD_BYTES) {
      toast.error(`Password must be at most ${MAX_PASSWORD_BYTES} bytes.`);
      return;
    }
    updateMutation.mutate({ id: u.id, payload: { password: next } });
  };

  const confirmDelete = (u: UserRecord) => {
    if (!window.confirm(`Delete user "${u.username}"? This cannot be undone.`)) {
      return;
    }
    deleteMutation.mutate(u.id);
  };

  const columns: ColumnDef<UserRow>[] = [
    {
      key: "username",
      header: "Username",
      render: (_value, row) => (
        <div className="flex items-center gap-2">
          <span className="text-sm font-medium text-white">{row.username}</span>
          {isSelf(row) && <Badge variant="muted">you</Badge>}
        </div>
      ),
    },
    {
      key: "role",
      header: "Role",
      className: "w-36",
      render: (_value, row) => (
        <select
          aria-label={`Role for ${row.username}`}
          value={row.role}
          disabled={isLastAdmin(row) || rowBusy(row.id)}
          title={
            isLastAdmin(row)
              ? "Cannot remove the last active admin"
              : "Change role"
          }
          onChange={(e) => changeRole(row, e.target.value as UserRole)}
          className={`${selectClass} disabled:cursor-not-allowed disabled:opacity-50`}
        >
          <option value="user">user</option>
          <option value="admin">admin</option>
        </select>
      ),
    },
    {
      key: "disabled",
      header: "Status",
      className: "w-28",
      render: (_value, row) => (
        <Badge variant={row.disabled ? "muted" : "success"}>
          {row.disabled ? "Disabled" : "Active"}
        </Badge>
      ),
    },
    {
      key: "created_at",
      header: "Created",
      className: "w-48",
      render: (_value, row) => (
        <span className="text-xs text-slate-400">
          {formatCreatedAt(row.created_at)}
        </span>
      ),
    },
    {
      key: "actions",
      header: "Actions",
      className: "w-72",
      render: (_value, row) => {
        const lastAdmin = isLastAdmin(row);
        const self = isSelf(row);
        return (
          <div className="flex flex-wrap items-center gap-2">
            <Button
              variant="ghost"
              size="sm"
              disabled={busy || (!row.disabled && lastAdmin)}
              title={
                !row.disabled && lastAdmin
                  ? "Cannot disable the last active admin"
                  : row.disabled
                    ? "Enable account"
                    : "Disable account"
              }
              onClick={() => toggleDisabled(row)}
            >
              {row.disabled ? "Enable" : "Disable"}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              disabled={busy}
              title="Reset password"
              onClick={() => resetPassword(row)}
            >
              Reset password
            </Button>
            <Button
              variant="danger"
              size="sm"
              disabled={busy || self || lastAdmin}
              title={
                self
                  ? "You cannot delete your own account"
                  : lastAdmin
                    ? "Cannot remove the last active admin"
                    : "Delete account"
              }
              onClick={() => confirmDelete(row)}
            >
              Delete
            </Button>
          </div>
        );
      },
    },
  ];

  return (
    <div className="space-y-4">
      <Card title="Create User">
        <form onSubmit={handleCreate} noValidate>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <FormGroup label="Username" htmlFor="new-user-username">
              <input
                id="new-user-username"
                type="text"
                autoComplete="off"
                placeholder="jane"
                value={newUsername}
                onChange={(e) => setNewUsername(e.target.value)}
                className={inputClass}
              />
            </FormGroup>

            <FormGroup label="Role" htmlFor="new-user-role">
              <select
                id="new-user-role"
                value={newRole}
                onChange={(e) => setNewRole(e.target.value as UserRole)}
                className={selectClass}
              >
                <option value="user">user</option>
                <option value="admin">admin</option>
              </select>
            </FormGroup>
          </div>

          <FormGroup
            label="Password"
            htmlFor="new-user-password"
            hint={`At least ${MIN_PASSWORD_LENGTH} characters, up to ${MAX_PASSWORD_BYTES} bytes. Use Generate for a strong temporary password.`}
          >
            <div className="flex gap-2">
              <div className="relative flex-1">
                <input
                  id="new-user-password"
                  type={showPassword ? "text" : "password"}
                  autoComplete="new-password"
                  placeholder="••••••••"
                  value={newPassword}
                  onChange={(e) => setNewPassword(e.target.value)}
                  className={`${inputClass} pr-10`}
                />
                <button
                  type="button"
                  onClick={() => setShowPassword((s) => !s)}
                  aria-label={showPassword ? "Hide password" : "Show password"}
                  className="absolute right-2 top-1/2 -translate-y-1/2 rounded p-1 text-slate-400 hover:text-slate-200"
                >
                  {showPassword ? <EyeOff size={16} /> : <Eye size={16} />}
                </button>
              </div>
              <Button
                type="button"
                variant="ghost"
                title="Generate a random password"
                onClick={() => {
                  setNewPassword(generatePassword());
                  setShowPassword(true);
                }}
              >
                <KeyRound size={14} />
                Generate
              </Button>
            </div>
          </FormGroup>

          {createError && (
            <div className="mt-3">
              <StatusMessage
                variant="error"
                message={createError}
                onDismiss={() => setCreateError(null)}
              />
            </div>
          )}

          <div className="mt-4">
            <Button
              type="submit"
              variant="primary"
              loading={createMutation.isPending}
            >
              Create User
            </Button>
          </div>
        </form>
      </Card>

      <Card title="Users">
        {usersQuery.isLoading ? (
          <div className="flex justify-center py-6">
            <Spinner />
          </div>
        ) : usersQuery.isError ? (
          <StatusMessage
            variant="error"
            message={
              usersQuery.error instanceof Error
                ? usersQuery.error.message
                : "Failed to load users."
            }
          />
        ) : (
          <DataTable<UserRow>
            columns={columns}
            data={users as UserRow[]}
            emptyMessage="No users."
            getRowKey={(row) => row.id}
          />
        )}
      </Card>
    </div>
  );
}
