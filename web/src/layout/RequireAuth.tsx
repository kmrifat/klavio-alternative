import { Navigate, Outlet, useLocation } from "react-router";
import { useMe } from "../api/hooks";
import { ErrorNote, Spinner } from "../components/ui";

// Renders child routes only for signed-in users; others go to /login?next=…
export function RequireAuth() {
  const me = useMe();
  const loc = useLocation();

  if (me.isPending) return <Spinner />;
  if (me.isError) {
    return (
      <div className="p-8">
        <ErrorNote error={me.error} />
      </div>
    );
  }
  if (!me.data) {
    const next = encodeURIComponent(loc.pathname + loc.search);
    return <Navigate to={`/login?next=${next}`} replace />;
  }
  return <Outlet />;
}
