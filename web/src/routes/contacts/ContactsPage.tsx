import { useState } from "react";
import { Link, useParams } from "react-router";
import { Lock, Plus, Search, Upload } from "lucide-react";
import { useContacts, useContactStats, type ContactFilters } from "../../api/contacts";
import { useBrands, useMe } from "../../api/hooks";
import type { Contact, Tier } from "../../api/types";
import { Badge, Button, ErrorNote, PageHeader, Spinner, inputBase } from "../../components/ui";
import { consentBasisLabel, engagementBadge, initials, shortDate, tierInfo, timeAgo } from "../../lib/format";
import { AddContactDialog, ContactDialog } from "./ContactDialogs";

const tierChoices: { value: string; label: string; tiers: Tier[] }[] = [
  { value: "default", label: "Tier: Active + Warm (default)", tiers: ["active", "warm"] },
  { value: "all", label: "Tier: all", tiers: [] },
  { value: "active", label: "Tier: Active", tiers: ["active"] },
  { value: "warm", label: "Tier: Warm", tiers: ["warm"] },
  { value: "at_risk", label: "Tier: At-risk", tiers: ["at_risk"] },
  { value: "sunset", label: "Tier: Sunset", tiers: ["sunset"] },
];

export function ContactsPage() {
  const slug = useParams().brand!;
  const me = useMe().data!;
  const brand = useBrands().data?.find((b) => b.slug === slug);
  const role = me.user.company_role === "owner" ? "brand_admin" : (brand?.my_role ?? "");
  const canSeePeople = role === "editor" || role === "sender" || role === "brand_admin";
  const isBrandAdmin = role === "brand_admin";

  const stats = useContactStats(slug);
  const [tierChoice, setTierChoice] = useState("default");
  const [status, setStatus] = useState("");
  const [tag, setTag] = useState("");
  const [q, setQ] = useState("");
  const filters: ContactFilters = {
    tiers: tierChoices.find((t) => t.value === tierChoice)!.tiers,
    status,
    tag: tag.trim(),
    q: q.trim(),
  };
  // Tiers only apply to subscribed people; picking another status shows them regardless of tier.
  if (status && status !== "subscribed") filters.tiers = [];
  const contacts = useContacts(slug, filters, canSeePeople);
  const [adding, setAdding] = useState(false);
  const [openId, setOpenId] = useState<string | null>(null);

  const rows = contacts.data?.pages.flatMap((p) => p.contacts) ?? [];
  const s = stats.data;

  return (
    <div className="max-w-6xl space-y-5">
      <PageHeader
        title="Contacts"
        description={
          <>
            {s ? `${s.total.toLocaleString()} ${brand?.name ?? ""} contacts. ` : ""}Only people with consent and recent engagement are mailable by
            default.
          </>
        }
        action={
          isBrandAdmin && (
            <div className="flex gap-2">
              <Link to="import">
                <Button>
                  <Upload className="size-4" /> Import (safety-checked)
                </Button>
              </Link>
              <Button variant="primary" onClick={() => setAdding(true)}>
                <Plus className="size-4" /> Add contact
              </Button>
            </div>
          )
        }
      />

      {stats.isError ? (
        <ErrorNote error={stats.error} />
      ) : (
        <div className="grid grid-cols-2 gap-3 md:grid-cols-5">
          {(["active", "warm", "at_risk", "sunset"] as Tier[]).map((t) => (
            <StatCard key={t} dot={tierInfo[t].tone} title={`${tierInfo[t].label} · ${tierInfo[t].range}`} value={s?.tiers[t]} help={tierInfo[t].help} />
          ))}
          <StatCard dot="blue" title="Quarantined imports" value={s?.quarantined} help="Re-permission email first" />
        </div>
      )}

      {!canSeePeople ? (
        <p className="flex items-center gap-2 rounded-lg border border-zinc-200 bg-zinc-50 px-4 py-3 text-sm text-zinc-600">
          <Lock className="size-4" /> Viewers see counts only — no personal data. Ask a brand admin for editor access to see contacts.
        </p>
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <label className="relative">
              <Search className="pointer-events-none absolute top-2.5 left-2.5 size-4 text-zinc-400" />
              <input
                aria-label="Search contacts"
                placeholder="Search by email or name…"
                className={`${inputBase} w-64 pl-8`}
                value={q}
                onChange={(e) => setQ(e.target.value)}
              />
            </label>
            <select aria-label="Tier" className={inputBase} value={tierChoice} onChange={(e) => setTierChoice(e.target.value)}>
              {tierChoices.map((t) => (
                <option key={t.value} value={t.value}>
                  {t.label}
                </option>
              ))}
            </select>
            <select aria-label="Status" className={inputBase} value={status} onChange={(e) => setStatus(e.target.value)}>
              <option value="">Status: any</option>
              <option value="subscribed">Subscribed</option>
              <option value="quarantined">Quarantined</option>
              <option value="pending_confirmation">Awaiting confirmation</option>
              <option value="unsubscribed">Unsubscribed</option>
            </select>
            <input aria-label="Tag" placeholder="Tag" className={`${inputBase} w-32`} value={tag} onChange={(e) => setTag(e.target.value)} />
            {s && (
              <span className="ml-auto flex items-center gap-1.5 text-xs text-zinc-500">
                <Lock className="size-3.5" /> {s.suppressed_company_wide.toLocaleString()} addresses suppressed company-wide · can't be re-imported
              </span>
            )}
          </div>

          <div className="overflow-hidden rounded-xl border border-zinc-200 bg-white">
            {contacts.isPending ? (
              <div className="px-5">
                <Spinner />
              </div>
            ) : contacts.isError ? (
              <div className="p-5">
                <ErrorNote error={contacts.error} />
              </div>
            ) : rows.length === 0 ? (
              <p className="px-5 py-10 text-center text-sm text-zinc-500">
                No contacts match.{isBrandAdmin && " Import a list or add someone to get started."}
              </p>
            ) : (
              <ContactsTable rows={rows} onOpen={setOpenId} />
            )}
            {contacts.hasNextPage && (
              <div className="border-t border-zinc-100 p-3 text-center">
                <Button size="sm" onClick={() => void contacts.fetchNextPage()} disabled={contacts.isFetchingNextPage}>
                  {contacts.isFetchingNextPage ? "Loading…" : "Load more"}
                </Button>
              </div>
            )}
          </div>
        </>
      )}

      <AddContactDialog slug={slug} open={adding} onClose={() => setAdding(false)} />
      {openId && <ContactDialog slug={slug} id={openId} role={role} onClose={() => setOpenId(null)} />}
    </div>
  );
}

function StatCard({ dot, title, value, help }: { dot: string; title: string; value: number | undefined; help: string }) {
  const dotColor = { green: "bg-green-500", amber: "bg-amber-500", red: "bg-red-500", blue: "bg-sky-500" }[dot];
  return (
    <div className="rounded-xl border border-zinc-200 bg-white px-4 py-3">
      <p className="flex items-center gap-1.5 text-xs text-zinc-600">
        <span className={`size-1.5 rounded-full ${dotColor}`} />
        {title}
      </p>
      <p className="mt-1 text-2xl font-semibold tabular-nums">{value === undefined ? "—" : value.toLocaleString()}</p>
      <p className="text-xs text-zinc-500">{help}</p>
    </div>
  );
}

function ContactsTable({ rows, onOpen }: { rows: Contact[]; onOpen: (id: string) => void }) {
  return (
    <table className="w-full text-sm">
      <thead>
        <tr className="border-b border-zinc-200 bg-zinc-50 text-left text-[11px] tracking-wide text-zinc-500 uppercase">
          <th className="px-5 py-2 font-medium">Contact</th>
          <th className="px-3 py-2 font-medium">Engagement</th>
          <th className="px-3 py-2 font-medium">Consent proof</th>
          <th className="px-5 py-2 text-right font-medium">Last click</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((c) => {
          const name = [c.first_name, c.last_name].filter(Boolean).join(" ");
          const badge = engagementBadge(c);
          const muted = !!c.suppression_reason || c.status === "unsubscribed";
          return (
            <tr
              key={c.id}
              onClick={() => onOpen(c.id)}
              className={`cursor-pointer border-b border-zinc-100 last:border-0 hover:bg-zinc-50 ${muted ? "text-zinc-400" : ""}`}
            >
              <td className="px-5 py-3">
                <div className="flex items-center gap-2.5">
                  <span className="grid size-7 shrink-0 place-items-center rounded-full bg-zinc-100 text-[11px] font-semibold text-zinc-600">
                    {initials(name || c.email)}
                  </span>
                  <div className="min-w-0">
                    <button type="button" className="font-medium hover:underline" onClick={() => onOpen(c.id)}>
                      {name || c.email}
                    </button>
                    {name && <p className="truncate text-xs text-zinc-500">{c.email}</p>}
                  </div>
                </div>
              </td>
              <td className="px-3 py-3">
                <Badge tone={badge.tone} dot>
                  {badge.label}
                </Badge>
              </td>
              <td className="px-3 py-3">
                <p className={muted ? "" : "text-zinc-900"}>{consentBasisLabel[c.consent_basis]}</p>
                <p className="max-w-xs truncate text-xs text-zinc-500">
                  {c.consented_at ? `${shortDate(c.consented_at)} · ` : ""}
                  {c.consent_source}
                </p>
              </td>
              <td className="px-5 py-3 text-right text-zinc-500">{c.last_click_at ? timeAgo(c.last_click_at) : "—"}</td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}
