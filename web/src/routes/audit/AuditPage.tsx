import { useState } from "react";
import { useSearchParams } from "react-router";
import { useAuditLog, type AuditFilters } from "../../api/governance";
import { useBrands } from "../../api/hooks";
import type { AuditEntry } from "../../api/types";
import { Button, ErrorNote, PageHeader, Spinner, inputBase } from "../../components/ui";
import { auditLabel } from "../../lib/format";

const actionGroups = [
  { value: "", label: "All actions" },
  { value: "auth.", label: "Sign-ins" },
  { value: "user.", label: "People & roles" },
  { value: "approval.", label: "Approvals" },
  { value: "safety_limit.", label: "Safety limits" },
  { value: "topic.", label: "Topics" },
  { value: "brand.", label: "Brands" },
  { value: "contact.", label: "Contacts" },
  { value: "import.", label: "Imports" },
  { value: "export.", label: "Exports" },
  { value: "alert.", label: "Alerts" },
];

const empty: AuditFilters = { brand: "", actor: "", action: "", since: "", until: "" };

export function AuditPage() {
  const brands = useBrands().data ?? [];
  // Filters can come from a link, e.g. /audit?action=safety_limit.
  const [params] = useSearchParams();
  const initial: AuditFilters = { ...empty, action: params.get("action") ?? "", brand: params.get("brand") ?? "" };
  const [draft, setDraft] = useState<AuditFilters>(initial);
  const [filters, setFilters] = useState<AuditFilters>(initial);
  const log = useAuditLog(filters);
  const rows = log.data?.pages.flatMap((p) => p.entries) ?? [];
  const brandName = (id: string | null) => brands.find((b) => b.id === id)?.name ?? "";
  const set = (k: keyof AuditFilters) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) => setDraft({ ...draft, [k]: e.target.value });

  return (
    <div className="max-w-6xl space-y-5">
      <PageHeader title="Audit log" description="Append-only: nobody — not even the app — can edit or delete an entry. Who, what, which brand, before → after, device and IP." />

      <form
        className="flex flex-wrap items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          setFilters(draft);
        }}
      >
        <select aria-label="Action" className={inputBase} value={draft.action} onChange={set("action")}>
          {actionGroups.map((g) => (
            <option key={g.value} value={g.value}>
              {g.label}
            </option>
          ))}
        </select>
        <select aria-label="Brand" className={inputBase} value={draft.brand} onChange={set("brand")}>
          <option value="">All brands</option>
          {brands.map((b) => (
            <option key={b.slug} value={b.slug}>
              {b.name}
            </option>
          ))}
        </select>
        <input aria-label="Person" placeholder="Person (email)" className={`${inputBase} w-48`} value={draft.actor} onChange={set("actor")} />
        <input aria-label="From" type="date" className={inputBase} value={draft.since} onChange={set("since")} />
        <input aria-label="To" type="date" className={inputBase} value={draft.until} onChange={set("until")} />
        <Button type="submit">Filter</Button>
        {JSON.stringify(filters) !== JSON.stringify(empty) && (
          <Button
            variant="ghost"
            onClick={() => {
              setDraft(empty);
              setFilters(empty);
            }}
          >
            Clear
          </Button>
        )}
      </form>

      <div className="overflow-hidden rounded-xl border border-zinc-200 bg-white">
        {log.isPending ? (
          <div className="px-5">
            <Spinner />
          </div>
        ) : log.isError ? (
          <div className="p-5">
            <ErrorNote error={log.error} />
          </div>
        ) : rows.length === 0 ? (
          <p className="px-5 py-10 text-center text-sm text-zinc-500">No entries match.</p>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-zinc-200 bg-zinc-50 text-left text-[11px] tracking-wide text-zinc-500 uppercase">
                <th className="px-5 py-2 font-medium">When</th>
                <th className="px-3 py-2 font-medium">Who</th>
                <th className="px-3 py-2 font-medium">What</th>
                <th className="px-3 py-2 font-medium">Brand</th>
                <th className="px-5 py-2 font-medium">Where from</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((e) => (
                <Row key={e.id} e={e} brand={brandName(e.brand_id)} />
              ))}
            </tbody>
          </table>
        )}
        {log.hasNextPage && (
          <div className="border-t border-zinc-100 p-3 text-center">
            <Button size="sm" onClick={() => void log.fetchNextPage()} disabled={log.isFetchingNextPage}>
              {log.isFetchingNextPage ? "Loading…" : "Older entries"}
            </Button>
          </div>
        )}
      </div>
    </div>
  );
}

// What to show after the action: approvals carry a readable summary.
function targetText(e: AuditEntry): string {
  const after = e.after as { summary?: unknown } | null | undefined;
  return typeof after?.summary === "string" ? after.summary : e.target_id;
}

function Row({ e, brand }: { e: AuditEntry; brand: string }) {
  const [open, setOpen] = useState(false);
  const hasDiff = e.before != null || e.after != null;
  return (
    <>
      <tr className="border-b border-zinc-100 align-top last:border-0">
        <td className="px-5 py-2.5 whitespace-nowrap text-zinc-600" title={e.at}>
          {new Date(e.at).toLocaleString()}
        </td>
        <td className="px-3 py-2.5">{e.actor_email || "system"}</td>
        <td className="px-3 py-2.5">
          <span className="font-medium">{auditLabel[e.action] ?? e.action}</span> {targetText(e) && <span className="text-zinc-600">{targetText(e)}</span>}
          {hasDiff && (
            <button type="button" className="ml-2 text-xs text-accent hover:underline" onClick={() => setOpen((o) => !o)} aria-expanded={open}>
              {open ? "hide details" : "details"}
            </button>
          )}
        </td>
        <td className="px-3 py-2.5 text-zinc-600">{brand}</td>
        <td className="px-5 py-2.5 font-mono text-xs text-zinc-500">{e.ip}</td>
      </tr>
      {open && (
        <tr className="border-b border-zinc-100 bg-zinc-50">
          <td colSpan={5} className="px-5 py-3">
            <div className="grid gap-3 text-xs sm:grid-cols-2">
              <div>
                <p className="mb-1 font-medium text-zinc-500">Before</p>
                <pre className="overflow-x-auto whitespace-pre-wrap">{JSON.stringify(e.before ?? null, null, 2)}</pre>
              </div>
              <div>
                <p className="mb-1 font-medium text-zinc-500">After</p>
                <pre className="overflow-x-auto whitespace-pre-wrap">{JSON.stringify(e.after ?? null, null, 2)}</pre>
              </div>
            </div>
          </td>
        </tr>
      )}
    </>
  );
}
