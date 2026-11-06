import { lazy, Suspense } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter, Route, Routes, Navigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { I18nProvider } from "@/app/providers";
import { AuthProvider, useAuth } from "@/app/auth-context";
import { TenantLayout } from "@/app/layouts/tenant-layout";
import { PlatformAdminLayout } from "@/app/layouts/platform-admin-layout";
import { hasMailboxSession } from "@/features/webmail/mailbox-api";
import { isWebmailNextEnabled } from "@/features/webmail-next/feature-flag";

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
const ArchivePage = lazy(() =>
  import("@/features/archive/archive-page").then((m) => ({
    default: m.ArchivePage,
  })),
);
const DeliveryPage = lazy(() =>
  import("@/features/delivery/delivery-page").then((m) => ({
    default: m.DeliveryPage,
  })),
);
const PlatformPage = lazy(() =>
  import("@/features/platform/platform-page").then((m) => ({
    default: m.PlatformPage,
  })),
);
const MarketingPage = lazy(() =>
  import("@/features/marketing/marketing-page").then((m) => ({
    default: m.MarketingPage,
  })),
);
const NotificationsPage = lazy(() =>
  import("@/features/notifications/notifications-page").then((m) => ({
    default: m.NotificationsPage,
  })),
);
const SupportPage = lazy(() =>
  import("@/features/support/support-page").then((m) => ({
    default: m.SupportPage,
  })),
);
const PlatformSupportPage = lazy(() =>
  import("@/features/support/support-page").then((m) => ({
    default: m.PlatformSupportPage,
  })),
);
const AISettingsPage = lazy(() =>
  import("@/features/ai/ai-settings-page").then((m) => ({
    default: m.AISettingsPage,
  })),
);
const SievePage = lazy(() =>
  import("@/features/sieve/sieve-page").then((m) => ({ default: m.SievePage })),
);

const WebmailHelpPage = lazy(() =>
  import("@/features/webmail/webmail-help-page").then((m) => ({
    default: m.WebmailHelpPage,
  })),
);
const MailboxStandaloneRead = lazy(() =>
  import("@/features/webmail/mailbox-standalone-read").then((m) => ({
    default: m.MailboxStandaloneRead,
  })),
);
const MailboxStandaloneCompose = lazy(() =>
  import("@/features/webmail/mailbox-standalone-compose").then((m) => ({
    default: m.MailboxStandaloneCompose,
  })),
);
const ClientGuidesPage = lazy(() =>
  import("@/features/guides/client-guides-page").then((m) => ({
    default: m.ClientGuidesPage,
  })),
);
const MailboxLoginPage = lazy(() =>
  import("@/features/webmail/mailbox-login-page").then((m) => ({
    default: m.MailboxLoginPage,
  })),
);
const MailboxInboxPage = lazy(() =>
  import("@/features/webmail/mailbox-inbox-page").then((m) => ({
    default: m.MailboxInboxPage,
  })),
);
const WebmailNextEntry = lazy(() =>
  import("@/features/webmail-next/webmail-next-entry").then((m) => ({
    default: m.WebmailNextEntry,
  })),
);
const MailboxSettingsPage = lazy(() =>
  import("@/features/webmail/mailbox-settings-page").then((m) => ({
    default: m.MailboxSettingsPage,
  })),
);
const MailboxPeoplePage = lazy(() =>
  import("@/features/webmail/mailbox-people-page").then((m) => ({
    default: m.MailboxPeoplePage,
  })),
);
const NotFoundPage = lazy(() =>
  import("@/features/not-found/not-found-page").then((m) => ({
    default: m.NotFoundPage,
  })),
);
const HomePage = lazy(() =>
  import("@/features/home/home-page").then((m) => ({
    default: m.HomePage,
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

function PlatformAdminRoute({ children }: { children: React.ReactNode }) {
  const { isAuthenticated, isLoading, isAdmin } = useAuth();
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
  if (!isAdmin) {
    return <Navigate to="/console" replace />;
  }
  return <PlatformAdminLayout>{children}</PlatformAdminLayout>;
}

function MailAliasRoute() {
  if (hasMailboxSession()) {
    return <Navigate to="/webmail/inbox" replace />;
  }
  return <Navigate to="/webmail/login" replace />;
}

function WebmailRoute() {
  if (hasMailboxSession()) {
    return <Navigate to="/webmail/inbox" replace />;
  }
  return <MailboxLoginPage />;
}

function WebmailInboxRoute() {
  if (isWebmailNextEnabled()) {
    return <WebmailNextEntry />;
  }
  return <MailboxInboxPage />;
}

export function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <I18nProvider>
        <AuthProvider>
          <BrowserRouter>
            <Routes>
              <Route
                path="/ai"
                element={
                  <ProtectedRoute>
                    <LazyPage>
                      <AISettingsPage />
                    </LazyPage>
                  </ProtectedRoute>
                }
              />
              <Route
                path="/mailbox/sieve"
                element={
                  <LazyPage>
                    <SievePage />
                  </LazyPage>
                }
              />
              <Route
                path="/mail"
                element={
                  <LazyPage>
                    <MailAliasRoute />
                  </LazyPage>
                }
              />
              <Route
                path="/webmail/login"
                element={
                  <LazyPage>
                    <MailboxLoginPage />
                  </LazyPage>
                }
              />
              <Route
                path="/webmail/inbox"
                element={
                  <LazyPage>
                    <WebmailInboxRoute />
                  </LazyPage>
                }
              />
              <Route
                path="/webmail/folder/:folderEnc"
                element={
                  <LazyPage>
                    <WebmailInboxRoute />
                  </LazyPage>
                }
              />
              <Route
                path="/webmail/settings"
                element={
                  <LazyPage>
                    <MailboxSettingsPage />
                  </LazyPage>
                }
              />
              <Route
                path="/webmail/people"
                element={
                  <LazyPage>
                    <MailboxPeoplePage />
                  </LazyPage>
                }
              />
              <Route
                path="/webmail/help"
                element={
                  <LazyPage>
                    <main className="mx-auto max-w-lg px-4 py-10">
                      <WebmailHelpPage />
                    </main>
                  </LazyPage>
                }
              />
              <Route
                path="/webmail/read"
                element={
                  <LazyPage>
                    <MailboxStandaloneRead />
                  </LazyPage>
                }
              />
              <Route
                path="/webmail/compose"
                element={
                  <LazyPage>
                    <MailboxStandaloneCompose />
                  </LazyPage>
                }
              />
              <Route
                path="/guides/clients"
                element={
                  <LazyPage>
                    <main className="mx-auto max-w-3xl px-4 py-10">
                      <ClientGuidesPage />
                    </main>
                  </LazyPage>
                }
              />
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
                  <LazyPage>
                    <HomePage />
                  </LazyPage>
                }
              />
              <Route
                path="/console"
                element={
                  <ProtectedRoute>
                    <LazyPage>
                      <DashboardPage />
                    </LazyPage>
                  </ProtectedRoute>
                }
              />
              <Route
                path="/archive"
                element={
                  <ProtectedRoute>
                    <LazyPage>
                      <ArchivePage />
                    </LazyPage>
                  </ProtectedRoute>
                }
              />
              <Route
                path="/delivery"
                element={
                  <ProtectedRoute>
                    <LazyPage>
                      <DeliveryPage />
                    </LazyPage>
                  </ProtectedRoute>
                }
              />
              <Route
                path="/marketing"
                element={
                  <ProtectedRoute>
                    <LazyPage>
                      <MarketingPage />
                    </LazyPage>
                  </ProtectedRoute>
                }
              />
              <Route
                path="/notifications"
                element={
                  <ProtectedRoute>
                    <LazyPage>
                      <NotificationsPage />
                    </LazyPage>
                  </ProtectedRoute>
                }
              />
              <Route
                path="/support"
                element={
                  <ProtectedRoute>
                    <LazyPage>
                      <SupportPage />
                    </LazyPage>
                  </ProtectedRoute>
                }
              />
              <Route
                path="/platform/support"
                element={
                  <PlatformAdminRoute>
                    <LazyPage>
                      <PlatformSupportPage />
                    </LazyPage>
                  </PlatformAdminRoute>
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
                path="/platform"
                element={
                  <PlatformAdminRoute>
                    <LazyPage>
                      <PlatformPage />
                    </LazyPage>
                  </PlatformAdminRoute>
                }
              />
              <Route
                path="/admin"
                element={
                  <PlatformAdminRoute>
                    <LazyPage>
                      <PlatformPage />
                    </LazyPage>
                  </PlatformAdminRoute>
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
                path="/webmail"
                element={
                  <LazyPage>
                    <WebmailRoute />
                  </LazyPage>
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
