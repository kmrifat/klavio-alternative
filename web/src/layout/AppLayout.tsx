import { useEffect } from "react";
import { NavLink, Outlet, useLocation, useParams } from "react-router";
import { Bell, Mail, Search } from "lucide-react";
import { brandScreens, companyScreens, settingsScreen, type Screen } from "../lib/screens";
import { findBrand, lastBrand, rememberBrand } from "../lib/brands";
import { BrandSwitcher } from "./BrandSwitcher";
import { ApiStatus } from "./ApiStatus";

export function AppLayout() {
  const params = useParams();
  const brand = findBrand(params.brand);
  const linkBrand = brand ?? lastBrand();

  useEffect(() => {
    if (brand) rememberBrand(brand.slug);
  }, [brand]);

  return (
    <div className="flex h-full">
      <aside className="flex w-56 shrink-0 flex-col border-r border-zinc-200 bg-sidebar px-2.5 py-3">
        <div className="flex items-center gap-2 px-1.5 pb-3">
          <span className="grid size-6 place-items-center rounded-md bg-zinc-900 text-white">
            <Mail className="size-3.5" />
          </span>
          <span className="text-sm font-medium">Acme Group</span>
        </div>

        <BrandSwitcher current={brand} />

        <NavSection label="Brand">
          {brandScreens.map((s) => (
            <NavItem key={s.id} screen={s} to={`/b/${linkBrand.slug}/${s.path}`} />
          ))}
        </NavSection>

        <NavSection label="Company">
          {companyScreens.map((s) => (
            <NavItem key={s.id} screen={s} to={`/${s.path}`} />
          ))}
        </NavSection>

        <div className="mt-auto border-t border-zinc-200 pt-2">
          <NavItem screen={settingsScreen} to={`/${settingsScreen.path}`} />
        </div>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <div className="h-1 shrink-0" style={{ background: brand?.color ?? "#18181b" }} />
        <TopBar brandName={brand?.name ?? "Acme Group"} />
        <main className="min-h-0 flex-1 overflow-y-auto px-8 py-7">
          <Outlet />
        </main>
      </div>
    </div>
  );
}

function NavSection({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <nav className="mt-5" aria-label={label}>
      <div className="px-2 pb-1.5 text-[11px] font-medium tracking-wide text-zinc-500 uppercase">{label}</div>
      <ul className="space-y-0.5">{children}</ul>
    </nav>
  );
}

function NavItem({ screen, to }: { screen: Screen; to: string }) {
  const Icon = screen.icon;
  return (
    <li>
      <NavLink
        to={to}
        className={({ isActive }) =>
          `flex items-center gap-2.5 rounded-md border px-2 py-1.5 text-sm ${
            isActive
              ? "border-zinc-300 bg-white font-medium text-zinc-900 shadow-xs"
              : "border-transparent text-zinc-700 hover:bg-zinc-100"
          }`
        }
      >
        <Icon className="size-4 text-zinc-500" />
        {screen.title}
      </NavLink>
    </li>
  );
}

function TopBar({ brandName }: { brandName: string }) {
  const { pathname } = useLocation();
  const screen = [...brandScreens, ...companyScreens, settingsScreen].find((s) => pathname.endsWith(`/${s.path}`));

  return (
    <header className="flex h-12 shrink-0 items-center gap-4 border-b border-zinc-200 px-6">
      <div className="truncate text-sm text-zinc-600">
        {brandName}
        {screen && <> / {screen.title}</>}
      </div>
      <div className="ml-auto flex items-center gap-3">
        <label className="flex w-60 items-center gap-2 rounded-md bg-zinc-100 px-2.5 py-1.5 text-sm text-zinc-400">
          <Search className="size-4" />
          <input
            disabled
            placeholder="Search…"
            aria-label="Search (coming later)"
            className="w-full bg-transparent outline-none"
          />
        </label>
        <ApiStatus />
        <Bell className="size-4 text-zinc-600" aria-hidden />
      </div>
    </header>
  );
}
