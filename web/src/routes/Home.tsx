import { Navigate } from "react-router";
import { useBrands, useMe } from "../api/hooks";
import { Spinner } from "../components/ui";
import { lastBrand } from "../lib/brands";

// Home: owners and approvers start at the Safety center (S1); brand members,
// who can't see company-wide figures, at their brand's health (S2).
export function Home() {
  const me = useMe().data;
  const brands = useBrands();
  if (!me) return null;
  if (seesCompany(me.user.company_role)) return <Navigate to="/safety" replace />;
  if (brands.isPending) return <Spinner />;
  const b = lastBrand(brands.data ?? []);
  if (b) return <Navigate to={`/b/${b.slug}/health`} replace />;
  return (
    <div className="max-w-xl">
      <h1 className="text-2xl font-semibold tracking-tight">No brands yet</h1>
      <p className="mt-1 text-sm text-zinc-600">An owner needs to give you access to a brand.</p>
    </div>
  );
}

// seesCompany: owners and approvers see company-wide screens like S1.
export function seesCompany(role: string | undefined) {
  return role === "owner" || role === "approver";
}
