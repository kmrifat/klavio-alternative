import { Navigate, Outlet, useParams, type RouteObject } from "react-router";
import { AppLayout } from "../layout/AppLayout";
import { ScreenPlaceholder } from "../routes/ScreenPlaceholder";
import { NotFound } from "../routes/NotFound";
import { brandScreens, companyScreens, settingsScreen } from "../lib/screens";
import { findBrand } from "../lib/brands";

function KnownBrand() {
  return findBrand(useParams().brand) ? <Outlet /> : <NotFound />;
}

export const routes: RouteObject[] = [
  {
    path: "/",
    element: <AppLayout />,
    children: [
      { index: true, element: <Navigate to="/safety" replace /> },
      ...[...companyScreens, settingsScreen].map((s) => ({
        path: s.path,
        element: <ScreenPlaceholder screen={s} />,
      })),
      {
        path: "b/:brand",
        element: <KnownBrand />,
        children: [
          { index: true, element: <Navigate to="campaigns" replace /> },
          ...brandScreens.map((s) => ({ path: s.path, element: <ScreenPlaceholder screen={s} /> })),
        ],
      },
      { path: "*", element: <NotFound /> },
    ],
  },
];
