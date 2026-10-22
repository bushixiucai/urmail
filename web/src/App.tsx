import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter, Route, Routes, Navigate } from "react-router-dom";
import { I18nProvider } from "@/app/providers";
import { AuthProvider, useAuth } from "@/app/auth-context";
import { TenantLayout } from "@/app/layouts/tenant-layout";
import { LoginPage } from "@/features/auth/login-page";
import { DashboardPage } from "@/features/dashboard/dashboard-page";
import { DomainListPage } from "@/features/domains/domain-list-page";
import { MailboxListPage } from "@/features/mailboxes/mailbox-list-page";
import { AliasListPage } from "@/features/aliases/alias-list-page";
import { NotFoundPage } from "@/features/not-found/not-found-page";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      retry: 1,
      refetchOnWindowFocus: false,
    },
  },
});

function ProtectedRoute({ children }: { children: React.ReactNode }) {
  const { isAuthenticated, isLoading } = useAuth();
  if (isLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center text-sm text-muted-foreground">
        加载中...
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
              <Route path="/login" element={<LoginPage />} />
              <Route
                path="/"
                element={
                  <ProtectedRoute>
                    <DashboardPage />
                  </ProtectedRoute>
                }
              />
              <Route
                path="/domains"
                element={
                  <ProtectedRoute>
                    <DomainListPage />
                  </ProtectedRoute>
                }
              />
              <Route
                path="/mailboxes"
                element={
                  <ProtectedRoute>
                    <MailboxListPage />
                  </ProtectedRoute>
                }
              />
              <Route
                path="/aliases"
                element={
                  <ProtectedRoute>
                    <AliasListPage />
                  </ProtectedRoute>
                }
              />
              <Route path="*" element={<NotFoundPage />} />
            </Routes>
          </BrowserRouter>
        </AuthProvider>
      </I18nProvider>
    </QueryClientProvider>
  );
}
