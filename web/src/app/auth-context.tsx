import {
  createContext,
  useContext,
  useEffect,
  useState,
  ReactNode,
} from "react";
import {
  clearAuth,
  getSessionToken,
  request,
  setCSRFToken,
  setSessionToken,
} from "@/lib/api/client";

export interface User {
  id: number;
  email: string;
  display_name: string;
  locale: string;
}

export interface TenantInfo {
  id: number;
  name: string;
  slug: string;
  status: string;
  role: string;
}

interface AuthState {
  isAuthenticated: boolean;
  isLoading: boolean;
  user: User | null;
  tenants: TenantInfo[];
  currentTenant: TenantInfo | null;
  isAdmin: boolean;
  login: (
    token: string,
    csrfToken: string,
    user: User,
    tenants: TenantInfo[],
    isAdmin: boolean,
  ) => void;
  logout: () => Promise<void>;
  switchTenant: (tenant: TenantInfo) => void;
}

const AuthContext = createContext<AuthState | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [isAuthenticated, setIsAuthenticated] =
    useState<boolean>(!!getSessionToken());
  const [isLoading, setIsLoading] = useState<boolean>(true);
  const [user, setUser] = useState<User | null>(null);
  const [tenants, setTenants] = useState<TenantInfo[]>([]);
  const [currentTenant, setCurrentTenant] = useState<TenantInfo | null>(null);
  const [isAdmin, setIsAdmin] = useState<boolean>(false);

  useEffect(() => {
    const token = getSessionToken();
    if (!token) {
      setIsLoading(false);
      return;
    }

    request<{ user: User; tenants: TenantInfo[]; is_admin: boolean }>(
      "/api/v1/auth/me",
    )
      .then((data) => {
        setUser(data.user);
        setTenants(data.tenants);
        if (data.tenants.length > 0) {
          setCurrentTenant(data.tenants[0]);
        }
        setIsAdmin(data.is_admin);
        setIsAuthenticated(true);
      })
      .catch(() => {
        clearAuth();
        setIsAuthenticated(false);
      })
      .finally(() => {
        setIsLoading(false);
      });
  }, []);

  const login = (
    token: string,
    csrfToken: string,
    u: User,
    t: TenantInfo[],
    admin: boolean,
  ) => {
    setSessionToken(token);
    setCSRFToken(csrfToken);
    setUser(u);
    setTenants(t);
    if (t.length > 0) {
      setCurrentTenant(t[0]);
    }
    setIsAdmin(admin);
    setIsAuthenticated(true);
  };

  const logout = async () => {
    try {
      await request("/api/v1/auth/logout", { method: "POST" });
    } catch {
      // Ignore error on logout
    } finally {
      clearAuth();
      setUser(null);
      setTenants([]);
      setCurrentTenant(null);
      setIsAuthenticated(false);
    }
  };

  const switchTenant = (t: TenantInfo) => {
    setCurrentTenant(t);
  };

  return (
    <AuthContext.Provider
      value={{
        isAuthenticated,
        isLoading,
        user,
        tenants,
        currentTenant,
        isAdmin,
        login,
        logout,
        switchTenant,
      }}
    >
      {children}
    </AuthContext.Provider>
  );
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext);
  if (!ctx) {
    throw new Error("useAuth must be used within an AuthProvider");
  }
  return ctx;
}
