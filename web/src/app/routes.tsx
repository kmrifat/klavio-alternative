import { Navigate, Outlet, useParams, type RouteObject } from "react-router";
import { useBrands } from "../api/hooks";
import { AppLayout } from "../layout/AppLayout";
import { RequireAuth } from "../layout/RequireAuth";
import { ScreenPlaceholder } from "../routes/ScreenPlaceholder";
import { NotFound } from "../routes/NotFound";
import { Login } from "../routes/auth/Login";
import { AcceptInvite } from "../routes/auth/AcceptInvite";
import { TeamPage } from "../routes/team/TeamPage";
import { SettingsLayout } from "../routes/settings/SettingsLayout";
import { BrandsList } from "../routes/settings/BrandsList";
import { NewBrand } from "../routes/settings/NewBrand";
import { BrandSettings } from "../routes/settings/BrandSettings";
import { TemplatesPage } from "../routes/templates/TemplatesPage";
import { TemplateEditor } from "../routes/templates/TemplateEditor";
import { LayoutEditor } from "../routes/templates/LayoutEditor";
import { CampaignsPage } from "../routes/campaigns/CampaignsPage";
import { CampaignPage } from "../routes/campaigns/CampaignPage";
import { CampaignReview } from "../routes/campaigns/CampaignReview";
import { ReportPage } from "../routes/campaigns/Feedback";
import { Spinner } from "../components/ui";
import { ContactsPage } from "../routes/contacts/ContactsPage";
import { ApprovalsPage } from "../routes/approvals/ApprovalsPage";
import { AuditPage } from "../routes/audit/AuditPage";
import { SafetyLimits } from "../routes/settings/SafetyLimits";
import { SendingProviders } from "../routes/settings/SendingProviders";
import { DataPrivacy } from "../routes/settings/DataPrivacy";
import { General } from "../routes/settings/General";
import { ApiKeysOverview } from "../routes/settings/ApiKeysOverview";
import { AiAgents } from "../routes/settings/AiAgents";
import { ConnectAgent } from "../routes/agents/ConnectAgent";
import { ImportWizard } from "../routes/contacts/ImportWizard";
import { SafetyCenter } from "../routes/safety/SafetyCenter";
import { Home } from "../routes/Home";
import { SegmentEditor, SegmentsPage } from "../routes/segments/SegmentsPage";
import { ContactsApiPage } from "../routes/api/ContactsApiPage";
import { BrandHealthPage } from "../routes/health/BrandHealthPage";
import { AccountBrakePage, RecoveryPage } from "../routes/health/RecoveryPage";
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
  safety: {
    path: "safety",
    children: [
      { index: true, element: <SafetyCenter /> },
      { path: "account-brake", element: <AccountBrakePage /> },
    ],
  },
};

const builtBrandScreens: Record<string, RouteObject> = {
  campaigns: {
    path: "campaigns",
    children: [
      { index: true, element: <CampaignsPage /> },
      { path: "new", element: <CampaignPage /> },
      { path: ":id", element: <CampaignPage /> },
      { path: ":id/review", element: <CampaignReview /> },
      { path: ":id/report", element: <ReportPage /> },
    ],
  },
  segments: {
    path: "segments",
    children: [
      { index: true, element: <SegmentsPage /> },
      { path: "new", element: <SegmentEditor /> },
      { path: ":id", element: <SegmentEditor /> },
    ],
  },
  api: { path: "api", element: <ContactsApiPage /> },
  health: {
    path: "health",
    children: [
      { index: true, element: <BrandHealthPage /> },
      { path: "recovery", element: <RecoveryPage /> },
    ],
  },
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
      // An AI client's sign-in: outside the app's layout, like the login page.
      { path: "connect/:id", element: <ConnectAgent /> },
      {
        element: <AppLayout />,
        children: [
          { index: true, element: <Home /> },
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
              { path: "providers", element: <SendingProviders /> },
              { path: "privacy", element: <DataPrivacy /> },
              { path: "general", element: <General /> },
              { path: "api-keys", element: <ApiKeysOverview /> },
              { path: "agents", element: <AiAgents /> },
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
