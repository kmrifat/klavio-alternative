import { useEffect } from "react";
import { NavLink, Outlet, useLocation, useNavigate, useParams } from "react-router";
import { LogOut, Mail, Search } from "lucide-react";
import { useApprovals } from "../api/governance";
import { AlertsBell } from "./AlertsBell";
import { useBrands, useMe, useSignOut } from "../api/hooks";
import type { BrandListItem } from "../api/types";
import { brandScreens, companyScreens, settingsScreen, type Screen } from "../lib/screens";
import { lastBrand, rememberBrand } from "../lib/brands";
import { brandRoleLabel, companyRoleLabel, initials } from "../lib/format";
import { BrandSwitcher } from "./BrandSwitcher";
import { seesCompany } from "../routes/Home";
import { ApiStatus } from "./ApiStatus";
import { HealthPill } from "./HealthPill";

export function AppLayout() {
  const params = useParams();
  const brands = useBrands().data ?? [];
  const brand = brands.find((b) => b.slug === params.brand);
  const linkBrand = brand ?? lastBrand(brands);
  const waitingCount = useApprovals("decide").data?.length;
  const role = useMe().data?.user.company_role;

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

        <BrandSwitcher brands={brands} current={brand} />

        <NavSection label="Brand">
          {brandScreens.map((s) => (
            <NavItem key={s.id} screen={s} to={linkBrand ? `/b/${linkBrand.slug}/${s.path}` : undefined} />
          ))}
        </NavSection>

        <NavSection label="Company">
          {companyScreens
            .filter((s) => s.path !== "safety" || seesCompany(role))
            .map((s) => (
              <NavItem key={s.id} screen={s} to={`/${s.path}`} count={s.path === "approvals" ? waitingCount : undefined} />
            ))}
        </NavSection>

        <div className="mt-auto border-t border-zinc-200 pt-2">
          <ul>
            <NavItem screen={settingsScreen} to={`/${settingsScreen.path}`} />
          </ul>
          <UserFooter brand={brand} />
        </div>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <div className="h-1 shrink-0" style={{ background: brand?.color ?? "#18181b" }} />
        <TopBar brandName={brand?.name ?? "Acme Group"} brandSlug={brand?.slug} />
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

// NavItem without `to` renders disabled (e.g. brand pages before any brand exists).
function NavItem({ screen, to, count }: { screen: Screen; to?: string; count?: number }) {
  const Icon = screen.icon;
  const base = "flex items-center gap-2.5 rounded-md border px-2 py-1.5 text-sm";
  if (!to) {
    return (
      <li>
        <span aria-disabled className={`${base} cursor-not-allowed border-transparent text-zinc-400`} title="Add a brand first">
          <Icon className="size-4" />
          {screen.title}
        </span>
      </li>
    );
  }
  return (
    <li>
      <NavLink
        to={to}
        className={({ isActive }) =>
          `${base} ${
            isActive
              ? "border-zinc-300 bg-white font-medium text-zinc-900 shadow-xs"
              : "border-transparent text-zinc-700 hover:bg-zinc-100"
          }`
        }
      >
        <Icon className="size-4 text-zinc-500" />
        {screen.title}
        {!!count && (
          <span className="ml-auto text-xs text-zinc-500" aria-label={`${count} waiting`}>
            {count}
          </span>
        )}
      </NavLink>
    </li>
  );
}

function UserFooter({ brand }: { brand: BrandListItem | undefined }) {
  const me = useMe().data;
  const signOut = useSignOut();
  const navigate = useNavigate();
  if (!me) return null;

  const role =
    me.user.company_role !== "member"
      ? `${companyRoleLabel[me.user.company_role]} · all brands`
      : brand?.my_role
        ? `${brandRoleLabel[brand.my_role]} · ${brand.name}`
        : "Member";

  return (
    <div className="mt-2 flex items-center gap-2 border-t border-zinc-200 px-1.5 pt-3">
      <span className="grid size-7 shrink-0 place-items-center rounded-full bg-indigo-50 text-[11px] font-semibold text-indigo-700">
        {initials(me.user.name)}
      </span>
      <span className="min-w-0 flex-1">
        <span className="block truncate text-sm font-medium">{me.user.name}</span>
        <span className="block truncate text-xs text-zinc-500">{role}</span>
      </span>
      <button
        type="button"
        aria-label="Sign out"
        title="Sign out"
        onClick={() => signOut.mutate(undefined, { onSuccess: () => navigate("/login") })}
        className="rounded p-1 text-zinc-400 hover:bg-zinc-100 hover:text-zinc-700"
      >
        <LogOut className="size-4" />
      </button>
    </div>
  );
}

function TopBar({ brandName, brandSlug }: { brandName: string; brandSlug?: string }) {
  const { pathname } = useLocation();
  const segments = pathname.split("/");
  // Brand pages live at /b/<slug>/<screen>/…, company pages at /<screen>/…
  const screen = [...brandScreens, ...companyScreens, settingsScreen].find((s) =>
    segments[1] === "b" ? segments[3] === s.path : segments[1] === s.path,
  );

  return (
    <header className="flex h-12 shrink-0 items-center gap-4 border-b border-zinc-200 px-6">
      <div className="truncate text-sm text-zinc-600">
        {brandName}
        {screen && <> / {screen.title}</>}
      </div>
      <div className="ml-auto flex items-center gap-3">
        <label className="flex w-60 items-center gap-2 rounded-md bg-zinc-100 px-2.5 py-1.5 text-sm text-zinc-400">
          <Search className="size-4" />
          <input disabled placeholder="Search…" aria-label="Search (coming later)" className="w-full bg-transparent outline-none" />
        </label>
        {brandSlug ? <HealthPill slug={brandSlug} /> : <ApiStatus />}
        <AlertsBell />
      </div>
    </header>
  );
}
