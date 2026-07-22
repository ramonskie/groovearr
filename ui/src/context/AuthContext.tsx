import {
  createContext,
  useContext,
  useState,
  useEffect,
  useCallback,
  type ReactNode,
} from "react";

// ─── Types ────────────────────────────────────────────────────────────

interface AuthState {
  isLoading: boolean;
  isAuthenticated: boolean;
  username: string | null;
  authMethod: string;
}

interface AuthContextValue extends AuthState {
  login: (username: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
}

// ─── Helpers ──────────────────────────────────────────────────────────

const API_KEY_KEY = "groovearr_api_key";

function getStoredApiKey(): string | null {
  try {
    return localStorage.getItem(API_KEY_KEY);
  } catch {
    return null;
  }
}

export function getApiKey(): string | null {
  return getStoredApiKey();
}

// ─── Context ──────────────────────────────────────────────────────────

const AuthContext = createContext<AuthContextValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<AuthState>({
    isLoading: true,
    isAuthenticated: false,
    username: null,
    authMethod: "",
  });

  // Check if we're already authenticated (session cookie or API key).
  const checkAuth = useCallback(async () => {
    try {
      const headers: Record<string, string> = {};
      const apiKey = getStoredApiKey();
      if (apiKey) headers["X-Api-Key"] = apiKey;

      const res = await fetch("/api/config", { headers });
      if (res.ok) {
        const cfg = await res.json() as { auth?: { method?: string; api_key?: string } };
        const method = cfg?.auth?.method || "";
        const key = cfg?.auth?.api_key;
        // Always store the API key so the SPA can use it for all requests.
        if (key) {
          try { localStorage.setItem(API_KEY_KEY, key); } catch {}
        }
        setState({ isLoading: false, isAuthenticated: true, username: null, authMethod: method });
        return;
      }
    } catch {
      // Network error — treat as unauthenticated.
    }
    setState({ isLoading: false, isAuthenticated: false, username: null, authMethod: "" });
  }, []);

  useEffect(() => {
    checkAuth();
  }, [checkAuth]);

  const login = useCallback(async (username: string, password: string) => {
    const res = await fetch("/api/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username, password }),
    });

    if (!res.ok) {
      const body = await res.json().catch(() => ({}));
      throw new Error((body as { error?: string }).error || "Login failed");
    }

    setState({ isLoading: false, isAuthenticated: true, username, authMethod: "forms" });
  }, []);

  const logout = useCallback(async () => {
    await fetch("/api/logout", { method: "POST" }).catch(() => {});
    localStorage.removeItem(API_KEY_KEY);
    setState({ isLoading: false, isAuthenticated: false, username: null, authMethod: "" });
  }, []);

  return (
    <AuthContext.Provider value={{ ...state, login, logout }}>
      {children}
    </AuthContext.Provider>
  );
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within AuthProvider");
  return ctx;
}
