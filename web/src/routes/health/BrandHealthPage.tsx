import { AlertTriangle, CheckCircle2, CircleHelp, RefreshCw, XCircle } from "lucide-react";
import { Link, useParams } from "react-router";
import { brakeInfo } from "../../api/brakes";
import { EventsCard } from "./RecoveryPage";
import {
  levelInfo,
  scoreLevel,
  useBrandHealth,
  useRecheckHealth,
  type BrandHealth,
  type CheckStatus,
  type HealthComponent,
  type WarmupStatus,
} from "../../api/health";
import { Badge, Button, Card, ErrorNote, PageHeader, Spinner } from "../../components/ui";
import { brandStatus, shortDate, timeAgo } from "../../lib/format";

const statusIcon: Record<CheckStatus, { icon: typeof CheckCircle2; color: string; label: string }> = {
  ok: { icon: CheckCircle2, color: "text-green-600", label: "OK" },
  warning: { icon: AlertTriangle, color: "text-amber-600", label: "Warning" },
  failing: { icon: XCircle, color: "text-red-600", label: "Failing" },
  unknown: { icon: CircleHelp, color: "text-zinc-400", label: "Unknown" },
};

// S2: one brand's health score, what it's made of, and how to fix it.
export function BrandHealthPage() {
  const slug = useParams().brand!;
  const q = useBrandHealth(slug);
  const recheck = useRecheckHealth(slug);
  if (q.isPending) return <Spinner />;
  if (q.isError) return <ErrorNote error={q.error} />;
  const d = q.data;
  const lv = levelInfo[d.health.level];
  const st = brandStatus[d.brand.status];
  const brake = d.brand.brake_state;

  return (
    <div className="max-w-5xl space-y-5">
      <PageHeader
        title="Brand health"
        description={
          <>
            {d.brand.name} sends from <span className="font-mono text-xs">{d.brand.sending_domain}</span>.{" "}
            {d.computed_at && <>Scored {timeAgo(d.computed_at)}; re-scored every hour.</>}
          </>
        }
        action={
          <Button onClick={() => recheck.mutate()} disabled={recheck.isPending}>
            <RefreshCw className={`size-4 ${recheck.isPending ? "animate-spin" : ""}`} />
            {recheck.isPending ? "Checking…" : "Re-check now"}
          </Button>
        }
      />
      <ErrorNote error={recheck.error} />
      {brake === "paused" && (
        <Link
          to="recovery"
          className="flex items-center justify-between gap-3 rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-900 hover:bg-red-100"
        >
          <span>
            <strong>{d.brand.name} is paused by its brake.</strong> No marketing email goes out until it's resumed.
          </span>
          <span className="font-medium whitespace-nowrap">Start recovery →</span>
        </Link>
      )}
      {(brake === "slowed" || brake === "watch") && (
        <Link
          to="recovery"
          className="flex items-center justify-between gap-3 rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-900 hover:bg-amber-100"
        >
          <span>
            <strong>{brakeInfo[brake].label}.</strong> {brakeInfo[brake].help} It only loosens with a second admin's approval.
          </span>
          <span className="font-medium whitespace-nowrap">Ask to loosen →</span>
        </Link>
      )}

      <section className="flex flex-wrap items-center gap-6 rounded-xl border border-zinc-200 bg-white p-5" aria-label="Health score">
        <div>
          <p className="text-xs tracking-wide text-zinc-500 uppercase">Health score</p>
          <p className={`text-5xl font-semibold tabular-nums ${lv.text}`}>
            {d.health.score ?? "—"}
            <span className="ml-1 text-base font-normal text-zinc-400">/ 100</span>
          </p>
        </div>
        <div className="min-w-0 flex-1 space-y-1">
          <div className="flex flex-wrap gap-2">
            <Badge tone={lv.tone} dot>
              {lv.label}
            </Badge>
            <Badge tone={st.tone} dot>
              {st.label}
            </Badge>
            {brake && brake !== "healthy" && (
              <span title={brakeInfo[brake].help}>
                <Badge tone={brakeInfo[brake].tone}>{brakeInfo[brake].label}</Badge>
              </span>
            )}
          </div>
          {d.health.weakest ? (
            <p className="text-sm text-zinc-700">
              <strong>Weakest area.</strong> {d.health.weakest}
            </p>
          ) : (
            d.health.score !== null && <p className="text-sm text-zinc-700">Every part is in good shape.</p>
          )}
          <p className="text-xs text-zinc-500">80+ is healthy, 60–79 needs watching, under 60 is at risk. Parts without data yet don't count.</p>
        </div>
      </section>

      <div className="grid gap-4 md:grid-cols-2">
        {d.health.components.map((c) => (
          <ComponentCard key={c.key} c={c} />
        ))}
      </div>

      <WarmupCard w={d.warmup} />

      <div className="grid gap-4 lg:grid-cols-2">
        <BlocklistCard d={d} />
        <GmailCard d={d} />
      </div>

      <EventsCard events={d.safety_events} />

      <Card title="Alerts" description="Alerts about this brand">
        {d.events.length === 0 ? (
          <p className="border-t border-zinc-200 px-5 py-4 text-sm text-zinc-500">Nothing yet.</p>
        ) : (
          <ul className="divide-y divide-zinc-100 border-t border-zinc-200">
            {d.events.map((e) => (
              <li key={e.id} className="flex items-start gap-3 px-5 py-2.5 text-sm">
                <Badge tone={e.severity === "critical" ? "red" : e.severity === "warning" ? "amber" : "blue"}>{e.severity}</Badge>
                <span className="flex-1">{e.message}</span>
                <span className="text-xs text-zinc-500">{timeAgo(e.created_at)}</span>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </div>
  );
}

function ComponentCard({ c }: { c: HealthComponent }) {
  const lv = levelInfo[scoreLevel(c.score)];
  return (
    <Card
      title={c.label}
      description={`${Math.round(c.weight * 100)}% of the score`}
      action={
        <span className={`text-2xl font-semibold tabular-nums ${lv.text}`} aria-label={`${c.label} score`}>
          {c.score ?? "—"}
        </span>
      }
    >
      <div className="border-t border-zinc-200 px-5 py-3">
        <p className="text-sm text-zinc-700">{c.summary}</p>
        {c.checks && c.checks.length > 0 && (
          <ul className="mt-2 space-y-1.5">
            {c.checks.map((ch) => {
              const s = statusIcon[ch.status];
              const Icon = s.icon;
              return (
                <li key={ch.label} className="flex items-start gap-2 text-sm">
                  <Icon className={`mt-0.5 size-4 shrink-0 ${s.color}`} aria-label={s.label} />
                  <div className="min-w-0">
                    <span className="font-medium">{ch.label}</span>
                    {ch.detail && <span className="text-zinc-500"> — {ch.detail}</span>}
                  </div>
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </Card>
  );
}

function WarmupCard({ w }: { w: WarmupStatus }) {
  return (
    <Card
      title="Warm-up"
      description="Mailbox providers trust a new domain slowly, so daily volume rises over about 3 weeks"
      action={w.complete ? <Badge tone="green">Complete</Badge> : <Badge tone="indigo">Day {w.day} of ~{w.days}</Badge>}
    >
      <div className="border-t border-zinc-200 px-5 py-4">
        {w.complete ? (
          <p className="text-sm text-zinc-700">No daily cap from warm-up any more. Safety brakes still apply.</p>
        ) : (
          <>
            <div className="flex justify-between text-sm">
              <span>{w.started_on ? "Sent today" : "Not started: day 1 begins with the first send"}</span>
              <span className="tabular-nums">
                {w.sent.toLocaleString()} / {w.cap.toLocaleString()}
              </span>
            </div>
            <div className="mt-1 h-2 overflow-hidden rounded-full bg-zinc-100">
              <div className="h-full bg-accent" style={{ width: `${Math.min(100, (w.sent / w.cap) * 100)}%` }} />
            </div>
            <p className="mt-2 text-xs text-zinc-500">
              Anything over today's cap waits for tomorrow. Every send during warm-up needs a second admin's approval.
            </p>
          </>
        )}
      </div>
    </Card>
  );
}

function BlocklistCard({ d }: { d: BrandHealth }) {
  const listed = d.blocklists.filter((b) => b.status === "listed");
  const checked = d.blocklists.map((b) => b.checked_at).sort()[0];
  return (
    <Card title="Blocklists" description="Spamhaus DBL, SURBL and URIBL, for the sending and link domains">
      <div className="border-t border-zinc-200 px-5 py-3 text-sm">
        {!d.blocklists_on ? (
          <p className="text-zinc-600">Blocklist checks are off. Set BLOCKLIST_RESOLVER to your own DNS resolver to turn them on.</p>
        ) : d.blocklists.length === 0 ? (
          <p className="text-zinc-600">Not checked yet. Checks run every few hours; “Re-check now” runs them at most once an hour.</p>
        ) : (
          <>
            <p className={listed.length ? "font-medium text-red-700" : "text-zinc-700"}>
              {listed.length ? `Listed on ${listed.length} list(s). Mail is likely going to spam.` : "Not listed anywhere."}
            </p>
            <table className="mt-2 w-full text-xs">
              <thead>
                <tr className="text-left text-zinc-500">
                  <th className="py-1 font-medium">Domain</th>
                  <th className="py-1 font-medium">List</th>
                  <th className="py-1 font-medium">Result</th>
                </tr>
              </thead>
              <tbody>
                {d.blocklists.map((b) => (
                  <tr key={b.zone + b.domain} className="border-t border-zinc-100">
                    <td className="py-1 font-mono">{b.domain}</td>
                    <td className="py-1">{b.zone}</td>
                    <td className="py-1" title={b.detail}>
                      <Badge tone={b.status === "clean" ? "green" : b.status === "listed" ? "red" : "zinc"}>{b.status}</Badge>
                      {b.status !== "clean" && b.detail && <span className="ml-1 text-zinc-500">{b.detail}</span>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {checked && <p className="mt-2 text-xs text-zinc-500">Checked {timeAgo(checked)}.</p>}
          </>
        )}
      </div>
    </Card>
  );
}

function pct(r: number) {
  return `${(r * 100).toFixed(r < 0.001 ? 3 : 2)}%`;
}

function GmailCard({ d }: { d: BrandHealth }) {
  const pm = d.postmaster;
  const latest = [...pm.history].reverse().find((h) => h.spam_ratio !== null || h.domain_reputation);
  return (
    <Card title="Gmail (Postmaster Tools)" description="Gmail's own spam rate and reputation for the sending domain">
      <div className="border-t border-zinc-200 px-5 py-3 text-sm">
        {pm.status === "not_connected" && (
          <p className="text-zinc-600">
            Not connected. An owner can connect it with a Google service account (POSTMASTER_CREDENTIALS_FILE) after verifying{" "}
            <span className="font-mono text-xs">{d.brand.sending_domain}</span> in Postmaster Tools.
          </p>
        )}
        {pm.status === "waiting" && <p className="text-zinc-600">Connected. The first daily pull hasn't run yet.</p>}
        {pm.status === "domain_not_found" && <p className="text-amber-700">{pm.detail || "This domain isn't set up in Postmaster Tools."}</p>}
        {pm.status === "error" && <p className="text-red-700">Couldn't read Postmaster Tools: {pm.detail}</p>}
        {pm.status === "ok" &&
          (latest ? (
            <>
              <div className="flex flex-wrap gap-6">
                <div>
                  <p className="text-xs text-zinc-500">Spam rate ({shortDate(latest.day)})</p>
                  <p className={`text-xl font-semibold ${latest.spam_ratio !== null && latest.spam_ratio >= 0.001 ? "text-red-700" : ""}`}>
                    {latest.spam_ratio === null ? "—" : pct(latest.spam_ratio)}
                  </p>
                </div>
                <div>
                  <p className="text-xs text-zinc-500">Domain reputation</p>
                  <p className="text-xl font-semibold">{latest.domain_reputation || "—"}</p>
                </div>
              </div>
              <p className="mt-2 text-xs text-zinc-500">Keep the spam rate under 0.1%. Gmail starts rejecting bulk mail at 0.3%.</p>
            </>
          ) : (
            <p className="text-zinc-600">No data in the last 30 days. Gmail only reports days with a few hundred emails to Gmail users.</p>
          ))}
      </div>
    </Card>
  );
}
