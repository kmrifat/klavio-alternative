import { AlertOctagon, AlertTriangle, ArrowRight, CheckCircle2, Info, ShieldCheck, ShieldOff } from "lucide-react";
import { Link } from "react-router";
import { levelInfo, scoreLevel, useSafetyCenter, type AccountMeters, type Attention, type BrandCard, type SafetyCenterData } from "../../api/health";
import { Badge, Card, ErrorNote, PageHeader, Spinner } from "../../components/ui";
import { brandStatus, timeAgo } from "../../lib/format";

// S1: every brand's health, the shared SES account, and what needs a human.
export function SafetyCenter() {
  const sc = useSafetyCenter();
  return (
    <div className="max-w-6xl space-y-5">
      <PageHeader
        title="Safety center"
        description="How every brand is doing, the AWS account they share, and anything that needs you."
      />
      {sc.isPending ? <Spinner /> : sc.isError ? <ErrorNote error={sc.error} /> : <Overview data={sc.data} />}
    </div>
  );
}

function Overview({ data: { brands, account, needs_attention, protections } }: { data: SafetyCenterData }) {
  return (
    <>
      <NeedsAttention items={needs_attention} />

      <section aria-label="Brands">
        <h2 className="mb-2 text-sm font-semibold text-zinc-700">Brands</h2>
        {brands.length === 0 ? (
          <p className="rounded-xl border border-dashed border-zinc-300 bg-zinc-50 p-5 text-sm text-zinc-600">
            No brands to show yet.{" "}
            <Link to="/settings/brands/new" className="text-accent underline">
              Add a brand
            </Link>
          </p>
        ) : (
          <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
            {brands.map((b) => (
              <BrandHealthCard key={b.slug} b={b} />
            ))}
          </div>
        )}
      </section>

      <div className="grid gap-4 lg:grid-cols-2">
        <AccountCard a={account} />
        <Card title="Company protections" description="Always-on rules that stop mistakes and misuse">
          <ul className="divide-y divide-zinc-100 border-t border-zinc-200">
            {protections.map((p) => (
              <li key={p.key} className="flex items-start gap-3 px-5 py-2.5">
                {p.on ? <ShieldCheck className="mt-0.5 size-4 shrink-0 text-green-600" /> : <ShieldOff className="mt-0.5 size-4 shrink-0 text-zinc-400" />}
                <div className="min-w-0 flex-1">
                  <p className="text-sm font-medium">{p.label}</p>
                  <p className="text-xs text-zinc-500">{p.detail}</p>
                </div>
                <Badge tone={p.on ? "green" : "zinc"}>{p.on ? "On" : "Off"}</Badge>
              </li>
            ))}
          </ul>
        </Card>
      </div>
    </>
  );
}

const severity = {
  critical: { icon: AlertOctagon, color: "text-red-600", tone: "red" as const, label: "Critical" },
  warning: { icon: AlertTriangle, color: "text-amber-600", tone: "amber" as const, label: "Warning" },
  info: { icon: Info, color: "text-sky-600", tone: "blue" as const, label: "Info" },
};

function NeedsAttention({ items }: { items: Attention[] }) {
  if (items.length === 0) {
    return (
      <p className="flex items-center gap-2 rounded-xl border border-green-200 bg-green-50 px-4 py-3 text-sm text-green-800">
        <CheckCircle2 className="size-4" /> Nothing needs your attention right now.
      </p>
    );
  }
  return (
    <Card title="Needs attention" description="Most urgent first">
      <ul className="divide-y divide-zinc-100 border-t border-zinc-200" aria-label="Needs attention">
        {items.map((it, i) => {
          const s = severity[it.severity];
          const Icon = s.icon;
          const body = (
            <>
              <Icon className={`mt-0.5 size-4 shrink-0 ${s.color}`} aria-label={s.label} />
              <div className="min-w-0 flex-1">
                <p className="text-sm font-medium">{it.title}</p>
                {it.detail && <p className="text-xs text-zinc-500">{it.detail}</p>}
              </div>
              {it.link && <ArrowRight className="mt-0.5 size-4 shrink-0 text-zinc-400" />}
            </>
          );
          return (
            <li key={`${it.kind}-${it.brand ?? ""}-${i}`}>
              {it.link ? (
                <Link to={it.link} className="flex items-start gap-3 px-5 py-2.5 hover:bg-zinc-50">
                  {body}
                </Link>
              ) : (
                <div className="flex items-start gap-3 px-5 py-2.5">{body}</div>
              )}
            </li>
          );
        })}
      </ul>
    </Card>
  );
}

function BrandHealthCard({ b }: { b: BrandCard }) {
  const lv = levelInfo[b.level];
  const st = brandStatus[b.status];
  return (
    <Link
      to={`/b/${b.slug}/health`}
      aria-label={`${b.name} health`}
      className="block rounded-xl border border-zinc-200 bg-white p-4 hover:border-zinc-300 hover:shadow-xs"
    >
      <div className="flex items-center gap-2">
        <span className="size-2.5 rounded-full" style={{ background: b.color }} />
        <span className="truncate text-sm font-semibold">{b.name}</span>
        <span className="ml-auto">
          <Badge tone={st.tone} dot>
            {st.label}
          </Badge>
        </span>
      </div>
      <div className="mt-3 flex items-end gap-2">
        <span className={`text-3xl font-semibold tabular-nums ${lv.text}`}>{b.score ?? "—"}</span>
        <span className="pb-1 text-xs text-zinc-500">/ 100 · {lv.label}</span>
      </div>
      <div className="mt-3 space-y-1.5">
        {b.components.map((c) => (
          <div key={c.key} className="flex items-center gap-2 text-xs">
            <span className="w-24 shrink-0 text-zinc-600">{c.label}</span>
            <span className="h-1.5 flex-1 overflow-hidden rounded-full bg-zinc-100">
              {c.score !== null && <span className={`block h-full ${levelInfo[scoreLevel(c.score)].bar}`} style={{ width: `${c.score}%` }} />}
            </span>
            <span className="w-7 text-right text-zinc-500 tabular-nums">{c.score ?? "—"}</span>
          </div>
        ))}
      </div>
      {b.weakest && <p className="mt-3 text-xs text-zinc-600">{b.weakest}</p>}
      <p className="mt-3 border-t border-zinc-100 pt-2 text-xs text-zinc-500">
        {b.warmup.complete ? (
          "Warm-up complete"
        ) : (
          <>
            Warm-up day {b.warmup.day} of ~{b.warmup.days} · {b.warmup.sent.toLocaleString()} / {b.warmup.cap.toLocaleString()} today
          </>
        )}
        {b.computed_at && <> · scored {timeAgo(b.computed_at)}</>}
      </p>
    </Link>
  );
}

function Meter({ label, used, total, help }: { label: string; used: number; total: number; help: string }) {
  const pct = total > 0 ? Math.min(100, (used / total) * 100) : 0;
  const bar = pct >= 80 ? "bg-red-500" : pct >= 60 ? "bg-amber-500" : "bg-accent";
  return (
    <div>
      <div className="flex justify-between text-sm">
        <span>{label}</span>
        <span className="text-zinc-600 tabular-nums">
          {Math.round(used).toLocaleString()} / {Math.round(total).toLocaleString()}
        </span>
      </div>
      <div className="mt-1 h-2 overflow-hidden rounded-full bg-zinc-100" role="meter" aria-label={label} aria-valuenow={Math.round(pct)} aria-valuemin={0} aria-valuemax={100}>
        <div className={`h-full ${bar}`} style={{ width: `${pct}%` }} />
      </div>
      <p className="mt-1 text-xs text-zinc-500">{help}</p>
    </div>
  );
}

function AccountCard({ a }: { a: AccountMeters }) {
  return (
    <Card title="Shared AWS SES account" description={`All brands send through one account (${a.region}); its limits and standing are shared`}>
      <div className="space-y-4 border-t border-zinc-200 px-5 py-4">
        {!a.available ? (
          <p className="text-sm text-red-700">{a.error ?? "Couldn't read the SES account."}</p>
        ) : (
          <>
            <div className="flex flex-wrap gap-2">
              <Badge tone={a.sending_enabled ? "green" : "red"} dot>
                {a.sending_enabled ? "Sending enabled" : "Sending disabled"}
              </Badge>
              <Badge tone={a.production_access ? "green" : "amber"} dot>
                {a.production_access ? "Production access" : "Sandbox"}
              </Badge>
              <Badge tone={a.enforcement_status === "HEALTHY" || !a.enforcement_status ? "green" : "red"} dot>
                Amazon status: {a.enforcement_status || "HEALTHY"}
              </Badge>
            </div>
            <Meter
              label="Sent in the last 24 hours"
              used={a.sent_last_24_hours}
              total={a.max_24_hour_send}
              help="Amazon's daily quota for the whole account, all brands and transactional mail included."
            />
            <p className="text-xs text-zinc-600">
              Marketing email sent today (UTC): <strong>{a.marketing_sent_today.toLocaleString()}</strong> · max rate {a.max_send_rate}/second
            </p>
          </>
        )}
      </div>
    </Card>
  );
}
