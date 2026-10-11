import { useQueries } from "@tanstack/react-query";
import { AlertTriangle, Info } from "lucide-react";
import { Link } from "react-router";
import { apiFetch } from "../../api/client";
import type { ApiKey } from "../../api/contactsApi";
import { useBrands } from "../../api/hooks";
import { Badge, Card, ErrorNote, Spinner } from "../../components/ui";
import { shortDate, timeAgo } from "../../lib/format";
import { stateBadge } from "../api/ContactsApiPage";

const expiringDays = 14;

// Settings → API keys: every brand's Contacts API keys in one list. Keys
// belong to a brand and are created and revoked on its Contacts API page.
export function ApiKeysOverview() {
  const brands = useBrands();
  // Keys are visible to brand admins (owners are brand admins everywhere).
  const mine = (brands.data ?? []).filter((b) => b.my_role === "brand_admin");
  const keys = useQueries({
    queries: mine.map((b) => ({
      queryKey: ["api-keys", b.slug],
      queryFn: () => apiFetch<{ keys: ApiKey[]; scopes: string[] }>(`/admin/v1/brands/${b.slug}/api-keys`),
    })),
  });

  if (brands.isPending || keys.some((k) => k.isPending)) return <Spinner />;
  if (brands.isError) return <ErrorNote error={brands.error} />;
  const failed = keys.find((k) => k.isError);

  const now = Date.now();
  const rows = mine.flatMap((b, i) => (keys[i]?.data?.keys ?? []).map((k) => ({ brand: b, key: k })));
  const live = rows.filter((r) => r.key.state === "active" || r.key.state === "pending_approval");
  const old = rows.filter((r) => !live.includes(r));
  const expiring = live.filter((r) => r.key.state === "active" && new Date(r.key.expires_at).getTime() - now < expiringDays * 86_400_000);

  return (
    <div className="space-y-4">
      <p className="flex gap-2 rounded-lg border border-sky-200 bg-sky-50 px-4 py-3 text-sm text-sky-900">
        <Info className="mt-0.5 size-4 shrink-0" />
        <span>
          <strong>API keys let a brand's website add people to its list.</strong> Each key belongs to one brand and lasts at most 90 days.
          Create and revoke them on the brand's <em>Contacts API</em> page.
        </span>
      </p>
      {failed && <ErrorNote error={failed.error} />}
      {expiring.length > 0 && (
        <p className="flex gap-2 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-900">
          <AlertTriangle className="mt-0.5 size-4 shrink-0" />
          <span>
            {expiring.length === 1 ? "1 key expires" : `${expiring.length} keys expire`} within {expiringDays} days. Make a new one and
            put it on the website before then, or sign-ups will stop working.
          </span>
        </p>
      )}
      {mine.length === 0 ? (
        <p className="text-sm text-zinc-600">API keys are managed by brand admins. You aren't a brand admin on any brand.</p>
      ) : (
        <>
          <KeysCard title="In use" description="Working keys, and new ones waiting for a second admin's approval" rows={live} now={now} empty="No working keys." />
          {old.length > 0 && <KeysCard title="Expired and revoked" description="Kept for reference; they no longer work" rows={old} now={now} />}
          <Card title="Manage keys by brand">
            <ul className="divide-y divide-zinc-100 border-t border-zinc-200 text-sm">
              {mine.map((b, i) => {
                const n = (keys[i]?.data?.keys ?? []).filter((k) => k.state === "active").length;
                return (
                  <li key={b.slug} className="flex items-center justify-between gap-3 px-5 py-2.5">
                    <span className="flex min-w-0 items-center gap-2">
                      <span className="size-2.5 shrink-0 rounded-full" style={{ background: b.color }} />
                      <span>
                        <span className="block">{b.name}</span>
                        <span className="block text-xs text-zinc-500">{n === 1 ? "1 working key" : `${n} working keys`}</span>
                      </span>
                    </span>
                    <Link to={`/b/${b.slug}/api`} className="shrink-0 whitespace-nowrap text-accent hover:underline">
                      Contacts API →
                    </Link>
                  </li>
                );
              })}
            </ul>
          </Card>
        </>
      )}
    </div>
  );
}

type Row = { brand: { slug: string; name: string; color: string }; key: ApiKey };

function KeysCard({ title, description, rows, now, empty }: { title: string; description: string; rows: Row[]; now: number; empty?: string }) {
  return (
    <Card title={title} description={description}>
      {rows.length === 0 ? (
        <p className="border-t border-zinc-200 px-5 py-4 text-sm text-zinc-600">{empty}</p>
      ) : (
        <table className="w-full text-sm">
          <thead>
            <tr className="border-y border-zinc-200 bg-zinc-50 text-left text-[11px] tracking-wide text-zinc-500 uppercase">
              <th className="px-5 py-2 font-medium">Key</th>
              <th className="px-3 py-2 font-medium">Brand</th>
              <th className="px-3 py-2 font-medium">Last used</th>
              <th className="px-3 py-2 font-medium">Expires</th>
              <th className="px-5 py-2 text-right font-medium">State</th>
            </tr>
          </thead>
          <tbody>
            {rows.map(({ brand: b, key: k }) => {
              const soon = k.state === "active" && new Date(k.expires_at).getTime() - now < expiringDays * 86_400_000;
              const st = stateBadge[k.state];
              return (
                <tr key={k.id} className="border-b border-zinc-100 last:border-0">
                  <td className="px-5 py-2.5">
                    <p className="font-medium">{k.name}</p>
                    <p className="font-mono text-xs text-zinc-500">{k.prefix}…</p>
                  </td>
                  <td className="px-3 py-2.5">
                    <Link to={`/b/${b.slug}/api`} className="flex items-center gap-1.5 hover:underline">
                      <span className="size-2 rounded-full" style={{ background: b.color }} />
                      {b.name}
                    </Link>
                  </td>
                  <td className="px-3 py-2.5 text-zinc-600">{k.last_used_at ? timeAgo(k.last_used_at) : "Never"}</td>
                  <td className={`px-3 py-2.5 ${soon ? "font-medium text-amber-700" : "text-zinc-600"}`}>{shortDate(k.expires_at)}</td>
                  <td className="px-5 py-2.5 text-right">
                    <Badge tone={st.tone} dot>
                      {st.label}
                    </Badge>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
    </Card>
  );
}
