import { lazy, Suspense } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter, Route, Routes, Navigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { I18nProvider } from "@/app/providers";
import { AuthProvider, useAuth } from "@/app/auth-context";
import { TenantLayout } from "@/app/layouts/tenant-layout";

const LoginPage = lazy(() =>
  import("@/features/auth/login-page").then((m) => ({ default: m.LoginPage })),
);
const DashboardPage = lazy(() =>
  import("@/features/dashboard/dashboard-page").then((m) => ({
    default: m.DashboardPage,
  })),
);
const DomainListPage = lazy(() =>
  import("@/features/domains/domain-list-page").then((m) => ({
    default: m.DomainListPage,
  })),
);
const MailboxListPage = lazy(() =>
  import("@/features/mailboxes/mailbox-list-page").then((m) => ({
    default: m.MailboxListPage,
  })),
);
const AliasListPage = lazy(() =>
  import("@/features/aliases/alias-list-page").then((m) => ({
    default: m.AliasListPage,
  })),
);
const MfaSettingsPage = lazy(() =>
  import("@/features/auth/mfa-settings-page").then((m) => ({
    default: m.MfaSettingsPage,
  })),
);
const AuditPage = lazy(() =>
  import("@/features/audit/audit-page").then((m) => ({
    default: m.AuditPage,
  })),
);
const BillingPage = lazy(() =>
  import("@/features/billing/billing-page").then((m) => ({
    default: m.BillingPage,
  })),
);
const PlatformPage = lazy(() =>
  import("@/features/platform/platform-page").then((m) => ({
    default: m.PlatformPage,
  })),
);
const NotFoundPage = lazy(() =>
  import("@/features/not-found/not-found-page").then((m) => ({
    default: m.NotFoundPage,
  })),
);

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      retry: 1,
      refetchOnWindowFocus: false,
    },
  },
});

function RouteFallback() {
  const { t } = useTranslation();
  return (
    <div className="min-h-screen flex items-center justify-center text-sm text-muted-foreground">
      {t("state.loading")}
    </div>
  );
}

function LazyPage({ children }: { children: React.ReactNode }) {
  return <Suspense fallback={<RouteFallback />}>{children}</Suspense>;
}

function ProtectedRoute({ children }: { children: React.ReactNode }) {
  const { isAuthenticated, isLoading } = useAuth();
  const { t } = useTranslation();
  if (isLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center text-sm text-muted-foreground">
        {t("state.loading")}
      </div>
    );
  }
  if (!isAuthenticated) {
    return <Navigate to="/login" replace />;
  }
  return <TenantLayout>{children}</TenantLayout>;
}

export function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <I18nProvider>
        <AuthProvider>
          <BrowserRouter>
            <Routes>
              <Route
                path="/login"
                element={
                  <LazyPage>
                    <LoginPage />
                  </LazyPage>
                }
              />
              <Route
                path="/"
                element={
                  <ProtectedRoute>
                    <LazyPage>
                      <DashboardPage />
                    </LazyPage>
                  </ProtectedRoute>
                }
              />
              <Route
                path="/domains"
                element={
                  <ProtectedRoute>
                    <LazyPage>
                      <DomainListPage />
                    </LazyPage>
                  </ProtectedRoute>
                }
              />
              <Route
                path="/mailboxes"
                element={
                  <ProtectedRoute>
                    <LazyPage>
                      <MailboxListPage />
                    </LazyPage>
                  </ProtectedRoute>
                }
              />
              <Route
                path="/aliases"
                element={
                  <ProtectedRoute>
                    <LazyPage>
                      <AliasListPage />
                    </LazyPage>
                  </ProtectedRoute>
                }
              />
              <Route
                path="/audit"
                element={
                  <ProtectedRoute>
                    <LazyPage>
                      <AuditPage />
                    </LazyPage>
                  </ProtectedRoute>
                }
              />
              <Route
                path="/billing"
                element={
                  <ProtectedRoute>
                    <LazyPage>
                      <BillingPage />
                    </LazyPage>
                  </ProtectedRoute>
                }
              />
              <Route
                path="/platform"
                element={
                  <ProtectedRoute>
                    <LazyPage>
                      <PlatformPage />
                    </LazyPage>
                  </ProtectedRoute>
                }
              />
              <Route
                path="/settings/mfa"
                element={
                  <ProtectedRoute>
                    <LazyPage>
                      <MfaSettingsPage />
                    </LazyPage>
                  </ProtectedRoute>
                }
              />
              <Route
                path="*"
                element={
                  <LazyPage>
                    <NotFoundPage />
                  </LazyPage>
                }
              />
            </Routes>
          </BrowserRouter>
        </AuthProvider>
      </I18nProvider>
    </QueryClientProvider>
  );
}
