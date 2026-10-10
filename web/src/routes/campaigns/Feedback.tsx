import { Link, useParams } from "react-router";
import { ArrowLeft, Bot, ShieldAlert } from "lucide-react";
import { botReasonLabel, pct, useMonitor, useReport, type Monitor } from "../../api/feedback";
import { Card, ErrorNote, PageHeader, Spinner } from "../../components/ui";
import { EventsCard } from "../health/RecoveryPage";

const fmt = (n: number) => n.toLocaleString();

// RateBar: a rate against the campaign brake's line.
function RateBar({ label, rate, limit, count }: { label: string; rate: number; limit: number; count: number }) {
  const share = limit > 0 ? Math.min(1, rate / limit) : 0;
  const tone = share >= 1 ? "bg-red-500" : share >= 0.6 ? "bg-amber-500" : "bg-green-500";
  return (
    <div>
      <div className="flex justify-between text-sm">
        <span>{label}</span>
        <span className="tabular-nums">
          {fmt(count)} · {pct(rate, 3)} <span className="text-xs text-zinc-500">/ pauses at {pct(limit, 3)}</span>
        </span>
      </div>
      <div className="mt-1 h-2 overflow-hidden rounded-full bg-zinc-100" role="meter" aria-label={label} aria-valuenow={Math.round(share * 100)} aria-valuemin={0} aria-valuemax={100}>
        <div className={`h-full ${tone}`} style={{ width: `${share * 100}%` }} />
      </div>
    </div>
  );
}

function Numbers({ m }: { m: Monitor }) {
  const e = m.engagement;
  const items: [string, number][] = [
    ["Delivered", m.feedback.delivered],
    ["Human clicks", e.human_clicks],
    ["Unique clickers", e.unique_clickers],
    ["Opens (est.)", e.human_opens_est],
  ];
  return (
    <dl className="grid grid-cols-2 gap-4 sm:grid-cols-4">
      {items.map(([label, n]) => (
        <div key={label}>
          <dt className="text-xs text-zinc-500">{label}</dt>
          <dd className="text-2xl font-semibold tabular-nums">{fmt(n)}</dd>
        </div>
      ))}
    </dl>
  );
}

function Bots({ m }: { m: Monitor }) {
  const e = m.engagement;
  if (e.bot_clicks === 0) return null;
  return (
    <p className="flex items-start gap-2 text-xs text-zinc-600">
      <Bot className="mt-0.5 size-3.5 shrink-0" />
      <span>
        {fmt(e.bot_clicks)} bot clicks filtered out (
        {Object.entries(e.bot_reasons)
          .map(([r, n]) => `${botReasonLabel[r] ?? r}: ${n}`)
          .join(", ")}
        ).
      </span>
    </p>
  );
}

// LiveMonitor (S7): the brakes' view of a running campaign, every 5 seconds.
export function LiveMonitor({ slug, id, live }: { slug: string; id: string; live: boolean }) {
  const q = useMonitor(slug, id, live);
  if (q.isPending) return <Spinner />;
  if (q.isError) return <ErrorNote error={q.error} />;
  const m = q.data;
  return (
    <Card
      title={live ? "Live monitor" : "Results so far"}
      description={live ? "Updates every few seconds. The campaign pauses itself if a line is crossed." : undefined}
      action={
        <Link to={`/b/${slug}/campaigns/${id}/report`} className="text-sm text-accent underline">
          Full report
        </Link>
      }
    >
      <div className="space-y-4 border-t border-zinc-100 px-5 py-4">
        {m.gate && !m.gate.allowed && (
          <p role="alert" className="flex items-start gap-2 rounded-md bg-red-50 px-3 py-2 text-sm text-red-900">
            <ShieldAlert className="mt-0.5 size-4 shrink-0" /> {m.gate.reason}
          </p>
        )}
        {m.gate && m.gate.allowed && m.gate.reason && <p className="rounded-md bg-amber-50 px-3 py-2 text-sm text-amber-900">{m.gate.reason}</p>}
        <Numbers m={m} />
        <div className="space-y-3">
          <RateBar label="Bounces" rate={m.rates.bounce} limit={m.limits.bounce} count={m.feedback.bounces} />
          <RateBar label="Complaints" rate={m.rates.complaint} limit={m.limits.complaint} count={m.feedback.complaints} />
          <RateBar label="Unsubscribes" rate={m.rates.unsubscribe} limit={m.limits.unsubscribe} count={m.feedback.unsubscribes} />
        </div>
        <Bots m={m} />
        {m.safety_events.length > 0 && (
          <ul className="space-y-1 text-sm">
            {m.safety_events.map((ev) => (
              <li key={ev.id} className="text-red-800">
                Brake: {ev.reason}
              </li>
            ))}
          </ul>
        )}
      </div>
    </Card>
  );
}

// ReportPage (S11): what a campaign did.
export function ReportPage() {
  const { brand: slug, id } = useParams() as { brand: string; id: string };
  const q = useReport(slug, id);
  if (q.isPending) return <Spinner />;
  if (q.isError) return <ErrorNote error={q.error} />;
  const r = q.data;
  const skipped = Object.values(r.sends.skipped).reduce((a, b) => a + b, 0);
  return (
    <div className="max-w-5xl space-y-5">
      <Link to={`/b/${slug}/campaigns/${id}`} className="inline-flex items-center gap-1 text-sm text-zinc-600 hover:text-zinc-900">
        <ArrowLeft className="size-4" /> {r.campaign.name}
      </Link>
      <PageHeader
        title="Campaign report"
        description={`${fmt(r.sends.sent)} sent · ${fmt(skipped)} skipped · ${fmt(r.sends.failed)} failed. Clicks are people; bots are counted separately. Opens are an estimate (Apple Mail loads every image).`}
      />
      <Card>
        <div className="space-y-4 px-5 py-4">
          <Numbers m={r} />
          <div className="space-y-3">
            <RateBar label="Bounces" rate={r.rates.bounce} limit={r.limits.bounce} count={r.feedback.bounces} />
            <RateBar label="Complaints" rate={r.rates.complaint} limit={r.limits.complaint} count={r.feedback.complaints} />
            <RateBar label="Unsubscribes" rate={r.rates.unsubscribe} limit={r.limits.unsubscribe} count={r.feedback.unsubscribes} />
          </div>
          <Bots m={r} />
        </div>
      </Card>

      <div className="grid gap-4 lg:grid-cols-2">
        <Card title="Top links" description="Human clicks; bots left out">
          {r.links.length === 0 ? (
            <p className="border-t border-zinc-100 px-5 py-4 text-sm text-zinc-500">No clicks yet.</p>
          ) : (
            <table className="w-full border-t border-zinc-100 text-sm">
              <thead>
                <tr className="text-left text-[11px] tracking-wide text-zinc-500 uppercase">
                  <th className="px-5 py-2 font-medium">Link</th>
                  <th className="px-3 py-2 text-right font-medium">Clicks</th>
                  <th className="px-5 py-2 text-right font-medium">People</th>
                </tr>
              </thead>
              <tbody>
                {r.links.map((l) => (
                  <tr key={l.link_no} className="border-t border-zinc-100">
                    <td className="max-w-0 truncate px-5 py-2" title={l.url}>
                      {l.url}
                    </td>
                    <td className="px-3 py-2 text-right tabular-nums">{fmt(l.human_clicks)}</td>
                    <td className="px-5 py-2 text-right tabular-nums">{fmt(l.unique_clickers)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </Card>
        <Card title="By mailbox provider" description="Where problems usually start">
          <table className="w-full border-t border-zinc-100 text-sm">
            <thead>
              <tr className="text-left text-[11px] tracking-wide text-zinc-500 uppercase">
                <th className="px-5 py-2 font-medium">Provider</th>
                <th className="px-2 py-2 text-right font-medium">Sent</th>
                <th className="px-2 py-2 text-right font-medium">Delivered</th>
                <th className="px-2 py-2 text-right font-medium">Bounced</th>
                <th className="px-2 py-2 text-right font-medium">Complaints</th>
                <th className="px-5 py-2 text-right font-medium">Clickers</th>
              </tr>
            </thead>
            <tbody>
              {r.providers.map((p) => (
                <tr key={p.provider} className="border-t border-zinc-100">
                  <td className="px-5 py-2">{p.provider}</td>
                  <td className="px-2 py-2 text-right tabular-nums">{fmt(p.sent)}</td>
                  <td className="px-2 py-2 text-right tabular-nums">{fmt(p.delivered)}</td>
                  <td className="px-2 py-2 text-right tabular-nums">{fmt(p.bounces)}</td>
                  <td className="px-2 py-2 text-right tabular-nums">{fmt(p.complaints)}</td>
                  <td className="px-5 py-2 text-right tabular-nums">{fmt(p.clickers)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </Card>
      </div>
      <EventsCard events={r.safety_events} />
    </div>
  );
}
