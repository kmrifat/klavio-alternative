import { NavLink, Outlet } from "react-router";
import { BookOpen, ExternalLink, Gauge, Globe, KeyRound, Lock, Server, Settings } from "lucide-react";
import { API_URL } from "../../api/client";
import { useMeta } from "../../api/hooks";
import { PageHeader } from "../../components/ui";

export const settingsTabs = [
  { path: "general", label: "General", icon: Settings, milestone: "M5" },
  { path: "brands", label: "Brands & domains", icon: Globe, milestone: "M1" },
  { path: "providers", label: "Sending providers", icon: Server, milestone: "M6" }, // built
  { path: "limits", label: "Safety limits", icon: Gauge, milestone: "M5" }, // built
  { path: "api-keys", label: "API keys", icon: KeyRound, milestone: "M7" },
  { path: "privacy", label: "Data & privacy", icon: Lock, milestone: "M5" }, // built
];

export function SettingsLayout() {
  // The admin API reference exists only while the server runs with DEBUG on.
  const debug = useMeta().data?.debug === true;
  return (
    <div className="max-w-6xl">
      <PageHeader title="Settings" description="Company-wide configuration. Changes need a passkey confirmation and are logged." />
      <div className="flex gap-8">
        <nav aria-label="Settings" className="w-48 shrink-0">
          <ul className="space-y-0.5">
            {settingsTabs.map(({ path, label, icon: Icon }) => (
              <li key={path}>
                <NavLink
                  to={path}
                  className={({ isActive }) =>
                    `flex items-center gap-2.5 rounded-md px-2 py-1.5 text-sm ${isActive ? "bg-indigo-50 font-medium text-indigo-700" : "text-zinc-700 hover:bg-zinc-100"}`
                  }
                >
                  <Icon className="size-4" />
                  {label}
                </NavLink>
              </li>
            ))}
            {debug && (
              <li className="mt-3 border-t border-zinc-200 pt-3">
                <a
                  href={`${API_URL}/admin/v1/docs`}
                  target="_blank"
                  rel="noreferrer"
                  className="flex items-center gap-2.5 rounded-md px-2 py-1.5 text-sm text-zinc-700 hover:bg-zinc-100"
                >
                  <BookOpen className="size-4" />
                  Admin API (debug)
                  <ExternalLink className="ml-auto size-3.5 text-zinc-400" />
                </a>
              </li>
            )}
          </ul>
        </nav>
        <div className="min-w-0 flex-1">
          <Outlet />
        </div>
      </div>
    </div>
  );
}

export function SettingsPlaceholder({ tab }: { tab: (typeof settingsTabs)[number] }) {
  return (
    <div className="rounded-xl border border-dashed border-zinc-300 bg-zinc-50 p-5 text-sm">
      <p className="font-medium">{tab.label}</p>
      <p className="mt-0.5 text-zinc-600">Built in milestone {tab.milestone}. See PLAN.md §7.</p>
    </div>
  );
}
