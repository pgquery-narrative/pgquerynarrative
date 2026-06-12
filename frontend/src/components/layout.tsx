import { NavLink, Outlet, useLocation } from "react-router-dom";
import { cn } from "@/lib/utils";
import { useTheme } from "@/contexts/theme-context";
import { LayoutDashboard, Terminal, Bookmark, FileText, Settings, PanelLeftClose, PanelLeft, Moon, Sun, PanelsTopLeft, CalendarClock, Activity } from "lucide-react";
import { useState } from "react";

const navItems = [
  { to: "/", icon: LayoutDashboard, label: "Dashboard" },
  { to: "/query", icon: Terminal, label: "Query Runner" },
  { to: "/stats", icon: Activity, label: "Query Stats" },
  { to: "/saved", icon: Bookmark, label: "Saved Queries" },
  { to: "/reports", icon: FileText, label: "Reports" },
  { to: "/dashboards", icon: PanelsTopLeft, label: "Dashboards" },
  { to: "/schedules", icon: CalendarClock, label: "Schedules" },
  { to: "/settings", icon: Settings, label: "Settings" },
];

export default function Layout() {
  const [collapsed, setCollapsed] = useState(false);
  const { theme, setTheme } = useTheme();
  const location = useLocation();
  const currentPageLabel =
    navItems.find((item) => item.to !== "/" && location.pathname.startsWith(item.to))?.label ||
    navItems.find((item) => item.to === location.pathname)?.label ||
    "Dashboard";

  return (
    <div className="flex h-screen overflow-hidden relative z-10 text-foreground">
      {/* Skip to main content for keyboard/screen reader users */}
      <a
        href="#main-content"
        className="sr-only focus-visible:not-sr-only focus-visible:fixed focus-visible:left-4 focus-visible:top-4 focus-visible:z-50 focus-visible:rounded-md focus-visible:bg-primary focus-visible:px-4 focus-visible:py-2 focus-visible:text-primary-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        Skip to main content
      </a>
      {/* Sidebar: glassmorphism-lite, theme-aware */}
      <aside
        className={cn(
          "flex flex-col border-r transition-all duration-200",
          "bg-card/90 backdrop-blur supports-[backdrop-filter]:bg-card/75",
          "border-border/70",
          collapsed ? "w-16" : "w-56"
        )}
      >
        <div className="flex items-center gap-3 px-4 py-4 border-b border-border/70">
          <img src="/logo.png" alt="Logo" className="h-8 w-8 flex-shrink-0" />
          {!collapsed && (
            <div className="min-w-0">
              <p className="text-sm font-semibold tracking-tight truncate">PgQueryNarrative</p>
              <p className="text-[11px] text-muted-foreground truncate">SQL to business narrative</p>
            </div>
          )}
        </div>

        <nav className="flex-1 py-3 space-y-1 px-2">
          {navItems.map(({ to, icon: Icon, label }) => (
            <NavLink
              key={to}
              to={to}
              end={to === "/"}
              className={({ isActive }) =>
                cn(
                  "flex items-center gap-3 rounded-lg px-3 py-2.5 text-sm font-medium transition-all duration-150",
                  isActive
                    ? "bg-primary/10 text-primary border border-primary/20"
                    : "text-muted-foreground hover:text-foreground hover:bg-muted/70 border border-transparent"
                )
              }
            >
              <Icon className="h-4 w-4 flex-shrink-0" />
              {!collapsed && <span>{label}</span>}
            </NavLink>
          ))}
        </nav>

        <div className="flex items-center border-t border-border/70">
          <button
            type="button"
            onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
            className={cn(
              "flex items-center justify-center text-muted-foreground hover:text-foreground hover:bg-muted/60 transition-colors cursor-pointer",
              collapsed ? "flex-1 py-3" : "flex-1 gap-2 py-3"
            )}
            title={theme === "dark" ? "Switch to light mode" : "Switch to dark mode"}
          >
            {theme === "dark" ? <Sun className="h-4 w-4 shrink-0" /> : <Moon className="h-4 w-4 shrink-0" />}
            {!collapsed && <span className="text-xs font-medium">{theme === "dark" ? "Light" : "Dark"}</span>}
          </button>
          <button
            onClick={() => setCollapsed(!collapsed)}
            className="flex items-center justify-center p-3 text-muted-foreground hover:text-foreground transition-colors cursor-pointer hover:bg-muted/60"
            title={collapsed ? "Expand sidebar" : "Collapse sidebar"}
          >
            {collapsed ? <PanelLeft className="h-4 w-4" /> : <PanelLeftClose className="h-4 w-4" />}
          </button>
        </div>
      </aside>

      {/* Main: content above background layers; id for skip link target */}
      <main id="main-content" className="flex-1 overflow-auto min-h-0 border-l border-transparent dark:border-border/30" tabIndex={-1}>
        <div className="sticky top-0 z-20 border-b border-border/70 bg-background/85 backdrop-blur supports-[backdrop-filter]:bg-background/70">
          <div className="max-w-7xl mx-auto px-5 md:px-8 py-3 flex items-center justify-between">
            <p className="text-sm font-semibold tracking-tight">{currentPageLabel}</p>
            <p className="text-xs text-muted-foreground">PgQueryNarrative</p>
          </div>
        </div>
        <div className="max-w-7xl mx-auto px-5 md:px-8 py-6 md:py-8">
          <Outlet />
        </div>
      </main>
    </div>
  );
}
