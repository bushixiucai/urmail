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
    currentTenantId?: number | null,
    remember?: boolean,
  ) => void;
  logout: () => Promise<void>;
  switchTenant: (tenant: TenantInfo) => Promise<void>;
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

    request<{
      user: User;
      tenants: TenantInfo[];
      is_admin: boolean;
      current_tenant_id?: number | null;
    }>("/api/v1/auth/me")
      .then((data) => {
        setUser(data.user);
        setTenants(data.tenants);
        const current =
          data.current_tenant_id != null
            ? data.tenants.find((t) => t.id === data.current_tenant_id)
            : undefined;
        setCurrentTenant(current ?? data.tenants[0] ?? null);
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
    currentTenantId?: number | null,
    remember = true,
  ) => {
    setSessionToken(token, remember);
    setCSRFToken(csrfToken, remember);
    setUser(u);
    setTenants(t);
    const current =
      currentTenantId != null
        ? t.find((tenant) => tenant.id === currentTenantId)
        : undefined;
    setCurrentTenant(current ?? t[0] ?? null);
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

  // Tenant switching is authoritative on the backend: the session's tenant
  // context only changes after POST /auth/switch-tenant succeeds.
  const switchTenant = async (tenant: TenantInfo) => {
    const previous = currentTenant;
    try {
      await request("/api/v1/auth/switch-tenant", {
        method: "POST",
        body: { tenant_id: tenant.id },
      });
      const me = await request<{
        user: User;
        tenants: TenantInfo[];
        is_admin: boolean;
        current_tenant_id?: number | null;
      }>("/api/v1/auth/me");
      setUser(me.user);
      setTenants(me.tenants);
      setIsAdmin(me.is_admin);
      const currentId = me.current_tenant_id ?? tenant.id;
      const match = me.tenants.find((t) => t.id === currentId);
      setCurrentTenant(match ?? tenant);
      // Re-mount the current route so every page refetches under the new
      // tenant context instead of showing the previous tenant's rows.
      window.location.reload();
    } catch {
      setCurrentTenant(previous);
    }
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
