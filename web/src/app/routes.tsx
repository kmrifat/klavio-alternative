import { Navigate, Outlet, useParams, type RouteObject } from "react-router";
import { useBrands } from "../api/hooks";
import { AppLayout } from "../layout/AppLayout";
import { RequireAuth } from "../layout/RequireAuth";
import { ScreenPlaceholder } from "../routes/ScreenPlaceholder";
import { NotFound } from "../routes/NotFound";
import { Login } from "../routes/auth/Login";
import { AcceptInvite } from "../routes/auth/AcceptInvite";
import { TeamPage } from "../routes/team/TeamPage";
import { SettingsLayout, SettingsPlaceholder, settingsTabs } from "../routes/settings/SettingsLayout";
import { BrandsList } from "../routes/settings/BrandsList";
import { NewBrand } from "../routes/settings/NewBrand";
import { BrandSettings } from "../routes/settings/BrandSettings";
import { TemplatesPage } from "../routes/templates/TemplatesPage";
import { TemplateEditor } from "../routes/templates/TemplateEditor";
import { LayoutEditor } from "../routes/templates/LayoutEditor";
import { Spinner } from "../components/ui";
import { ContactsPage } from "../routes/contacts/ContactsPage";
import { ApprovalsPage } from "../routes/approvals/ApprovalsPage";
import { AuditPage } from "../routes/audit/AuditPage";
import { SafetyLimits } from "../routes/settings/SafetyLimits";
import { ImportWizard } from "../routes/contacts/ImportWizard";
import { SafetyCenter } from "../routes/safety/SafetyCenter";
import { BrandHealthPage } from "../routes/health/BrandHealthPage";
import { brandScreens, companyScreens } from "../lib/screens";

// KnownBrand renders brand pages only for brands the user can see.
function KnownBrand() {
  const slug = useParams().brand;
  const brands = useBrands();
  if (brands.isPending) return <Spinner />;
  return brands.data?.some((b) => b.slug === slug) ? <Outlet /> : <NotFound />;
}

const builtCompanyScreens: Record<string, RouteObject> = {
  team: { path: "team", element: <TeamPage /> },
  approvals: { path: "approvals", element: <ApprovalsPage /> },
  audit: { path: "audit", element: <AuditPage /> },
  safety: { path: "safety", element: <SafetyCenter /> },
};

const builtBrandScreens: Record<string, RouteObject> = {
  health: { path: "health", element: <BrandHealthPage /> },
  templates: {
    path: "templates",
    children: [
      { index: true, element: <TemplatesPage /> },
      { path: "new", element: <TemplateEditor /> },
      { path: "layout", element: <LayoutEditor /> },
      { path: ":id", element: <TemplateEditor /> },
    ],
  },
};

export const routes: RouteObject[] = [
  { path: "/login", element: <Login /> },
  { path: "/invite/:token", element: <AcceptInvite /> },
  {
    path: "/",
    element: <RequireAuth />,
    children: [
      {
        element: <AppLayout />,
        children: [
          { index: true, element: <Navigate to="/safety" replace /> },
          ...companyScreens.map(
            (s) => builtCompanyScreens[s.path] ?? { path: s.path, element: <ScreenPlaceholder screen={s} /> },
          ),
          {
            path: "settings",
            element: <SettingsLayout />,
            children: [
              { index: true, element: <Navigate to="brands" replace /> },
              { path: "brands", element: <BrandsList /> },
              { path: "brands/new", element: <NewBrand /> },
              { path: "brands/:slug", element: <BrandSettings /> },
              { path: "limits", element: <SafetyLimits /> },
              ...settingsTabs
                .filter((t) => t.path !== "brands" && t.path !== "limits")
                .map((t) => ({ path: t.path, element: <SettingsPlaceholder tab={t} /> })),
            ],
          },
          {
            path: "b/:brand",
            element: <KnownBrand />,
            children: [
              { index: true, element: <Navigate to="campaigns" replace /> },
              { path: "contacts", element: <ContactsPage /> },
              { path: "contacts/import", element: <ImportWizard /> },
              { path: "contacts/import/:id", element: <ImportWizard /> },
              ...brandScreens
                .filter((s) => s.path !== "contacts")
                .map((s) => builtBrandScreens[s.path] ?? { path: s.path, element: <ScreenPlaceholder screen={s} /> }),
            ],
          },
          { path: "*", element: <NotFound /> },
        ],
      },
    ],
  },
];
