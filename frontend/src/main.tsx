import { StrictMode, lazy, Suspense } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter, Routes, Route } from "react-router";
import "@/styles/globals.css";
import { ThemeProvider } from "@/contexts/theme-context";
import { AnnounceProvider } from "@/contexts/announce-context";
import { ErrorBoundary } from "@/components/error-boundary";
import { RouteFallback } from "@/components/route-fallback";
import Layout from "@/components/layout";
import Dashboard from "@/pages/dashboard";

const QueryRunner = lazy(() => import("@/pages/query-runner"));
const SavedQueries = lazy(() => import("@/pages/saved-queries"));
const Reports = lazy(() => import("@/pages/reports"));
const SettingsPage = lazy(() => import("@/pages/settings"));
const DashboardsPage = lazy(() => import("@/pages/dashboards"));
const SchedulesPage = lazy(() => import("@/pages/schedules"));
const QueryStatsPage = lazy(() => import("@/pages/query-stats"));

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ErrorBoundary>
      <ThemeProvider>
        <AnnounceProvider>
          <BrowserRouter>
            <Routes>
              <Route element={<Layout />}>
                <Route index element={<Dashboard />} />
                <Route
                  path="dashboards"
                  element={
                    <Suspense fallback={<RouteFallback />}>
                      <DashboardsPage />
                    </Suspense>
                  }
                />
                <Route
                  path="dashboards/:id"
                  element={
                    <Suspense fallback={<RouteFallback />}>
                      <DashboardsPage />
                    </Suspense>
                  }
                />
                <Route
                  path="query"
                  element={
                    <Suspense fallback={<RouteFallback />}>
                      <QueryRunner />
                    </Suspense>
                  }
                />
                <Route
                  path="stats"
                  element={
                    <Suspense fallback={<RouteFallback />}>
                      <QueryStatsPage />
                    </Suspense>
                  }
                />
                <Route
                  path="schedules"
                  element={
                    <Suspense fallback={<RouteFallback />}>
                      <SchedulesPage />
                    </Suspense>
                  }
                />
                <Route
                  path="saved"
                  element={
                    <Suspense fallback={<RouteFallback />}>
                      <SavedQueries />
                    </Suspense>
                  }
                />
                <Route
                  path="reports"
                  element={
                    <Suspense fallback={<RouteFallback />}>
                      <Reports />
                    </Suspense>
                  }
                />
                <Route
                  path="reports/:id"
                  element={
                    <Suspense fallback={<RouteFallback />}>
                      <Reports />
                    </Suspense>
                  }
                />
                <Route
                  path="shared/:token"
                  element={
                    <Suspense fallback={<RouteFallback />}>
                      <Reports />
                    </Suspense>
                  }
                />
                <Route
                  path="settings"
                  element={
                    <Suspense fallback={<RouteFallback />}>
                      <SettingsPage />
                    </Suspense>
                  }
                />
              </Route>
            </Routes>
          </BrowserRouter>
        </AnnounceProvider>
      </ThemeProvider>
    </ErrorBoundary>
  </StrictMode>
);
