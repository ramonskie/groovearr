import {
  createContext,
  useContext,
  useState,
  useEffect,
  useCallback,
  type ReactNode,
} from "react";
import {
  getMe,
  login as apiLogin,
  logout as apiLogout,
  ApiRequestError,
} from "../api/client";
import type { MeResponse } from "../api/types";

// ─── Types ────────────────────────────────────────────────────────────

interface AuthState {
  isLoading: boolean;
  isAuthenticated: boolean;
  username: string | null;
  /** Coarse account role from GET /api/me ("admin" | "user"); null if unknown. */
  role: string | null;
  authMethod: string;
}

interface AuthContextValue extends AuthState {
  /** Convenience accessor: true only when the resolved role is "admin". */
  isAdmin: boolean;
  login: (username: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
}

// ─── Helpers ──────────────────────────────────────────────────────────

/**
 * Derive the shell's auth method from the identity response. GET /api/me does
 * not report the configured method, so infer it: an API-key caller, else a
 * session username, else "none" (fully open — no logout shown).
 */
function authMethodFromMe(me: MeResponse): string {
  if (me.via_api_key) return "api_key";
  if (me.username) return "forms";
  return "none";
}

// ─── Context ──────────────────────────────────────────────────────────

const AuthContext = createContext<AuthContextValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<AuthState>({
    isLoading: true,
    isAuthenticated: false,
    username: null,
    role: null,
    authMethod: "",
  });

  // Check if we're already authenticated (session cookie).
  // All HTTP goes through the api client (rule: no raw fetch in components).
  const checkAuth = useCallback(async () => {
    try {
      const me = await getMe();
      setState({
        isLoading: false,
        isAuthenticated: true,
        username: me.username || null,
        role: me.role,
        authMethod: authMethodFromMe(me),
      });
    } catch (err) {
      // A 403 means the caller IS authenticated but lacks permission for the
      // endpoint. Never treat it as unauthenticated: doing so caused a login
      // loop when the check hit admin-only GET /api/config (finding C2).
      if (err instanceof ApiRequestError && err.status === 403) {
        setState({
          isLoading: false,
          isAuthenticated: true,
          username: null,
          role: null,
          authMethod: "",
        });
        return;
      }
      // 401 (or network error): unauthenticated. request() already redirects
      // to /login on a 401 outside the login page.
      setState({
        isLoading: false,
        isAuthenticated: false,
        username: null,
        role: null,
        authMethod: "",
      });
    }
  }, []);

  useEffect(() => {
    checkAuth();
  }, [checkAuth]);

  const login = useCallback(async (username: string, password: string) => {
    await apiLogin(username, password);
    // The login response carries no identity, so resolve the role (and the
    // canonical username) from GET /api/me right away — otherwise admin-only
    // UI would stay hidden until the next mount-time auth check. On failure
    // stay least-privileged: role null => treated as non-admin.
    let role: string | null = null;
    let name = username;
    try {
      const me = await getMe();
      role = me.role;
      name = me.username || username;
    } catch {
      // Resolving identity failed; a later auth check will correct the role.
    }
    setState({
      isLoading: false,
      isAuthenticated: true,
      username: name,
      role,
      authMethod: "forms",
    });
  }, []);

  const logout = useCallback(async () => {
    await apiLogout().catch(() => {});
    setState({
      isLoading: false,
      isAuthenticated: false,
      username: null,
      role: null,
      authMethod: "",
    });
  }, []);

  return (
    <AuthContext.Provider
      value={{ ...state, isAdmin: state.role === "admin", login, logout }}
    >
      {children}
    </AuthContext.Provider>
  );
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within AuthProvider");
  return ctx;
}
