import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { AlertTriangle, ArrowLeft, Send } from "lucide-react";
import { ApiError } from "../../api/client";
import { useTopics } from "../../api/contacts";
import { useTemplates } from "../../api/templates";
import {
  useCampaign,
  useCampaignAction,
  useSaveCampaign,
  useSeeds,
  useTestSend,
  type Campaign,
  type CampaignResponse,
  type DraftInput,
} from "../../api/campaigns";
import { Button, Card, ErrorNote, Field, inputClass, Spinner } from "../../components/ui";
import { timeAgo } from "../../lib/format";
import { NotFound } from "../NotFound";
import { useBrandAccess } from "../templates/parts";
import { AudienceBreakdown, breakdownOf, fmt, StateBadge, summaryOf } from "./parts";
import { LiveMonitor } from "./Feedback";

export function CampaignPage() {
  const { brand: slug, id } = useParams() as { brand: string; id?: string };
  const c = useCampaign(slug, id);
  if (id && c.isPending) return <Spinner />;
  if (id && c.isError) return c.error instanceof ApiError && c.error.status === 404 ? <NotFound /> : <ErrorNote error={c.error} />;
  const data = c.data;
  if (data && !["draft", "needs_approval"].includes(data.campaign.state)) {
    return <CampaignStatus slug={slug} data={data} />;
  }
  return <Editor key={id ?? "new"} slug={slug} id={id} data={data} />;
}

function Back({ slug }: { slug: string }) {
  return (
    <Link to={`/b/${slug}/campaigns`} className="inline-flex items-center gap-1 text-sm text-zinc-600 hover:text-zinc-900">
      <ArrowLeft className="size-4" /> Campaigns
    </Link>
  );
}

// Editor: a draft's content and audience (S10), with the live breakdown (S5).
function Editor({ slug, id, data }: { slug: string; id?: string; data?: CampaignResponse }) {
  const navigate = useNavigate();
  const { canEdit, canSend } = useBrandAccess(slug);
  const topics = useTopics(slug);
  const templates = useTemplates(slug);
  const save = useSaveCampaign(slug, id);
  const c = data?.campaign;
  const [form, setForm] = useState<DraftInput>({
    name: c?.name ?? "",
    topic_id: c?.topic_id ?? "",
    template_id: c?.template_id ?? null,
    subject: c?.subject ?? "",
    preheader: c?.preheader ?? "",
    include_at_risk: c?.include_at_risk ?? false,
  });
  const topicID = form.topic_id || topics.data?.[0]?.id || "";
  const dirty =
    !c ||
    form.name !== c.name ||
    topicID !== c.topic_id ||
    form.template_id !== c.template_id ||
    form.subject !== c.subject ||
    form.preheader !== c.preheader ||
    form.include_at_risk !== c.include_at_risk;
  const usable = templates.data?.filter((t) => t.error_count === 0 && !t.stale) ?? [];

  return (
    <div className="max-w-5xl space-y-4">
      <Back slug={slug} />
      {c?.state === "needs_approval" && (
        <p role="note" className="flex gap-2 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-900">
          <AlertTriangle className="mt-0.5 size-4 shrink-0" />
          Waiting for a second person to approve it (see Approvals). Editing it withdraws the request.
        </p>
      )}
      <form
        className="grid gap-4 lg:grid-cols-[1fr_320px]"
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate(
            { ...form, topic_id: topicID, ...(c ? { version: c.version } : {}) },
            { onSuccess: (d) => !id && navigate(`/b/${slug}/campaigns/${d.campaign.id}`, { replace: true }) },
          );
        }}
      >
        <Card title={c ? c.name : "New campaign"} description={c ? `Draft · saved ${timeAgo(c.updated_at)}` : "One template, to one topic."}>
          <div className="space-y-4 border-t border-zinc-100 px-5 py-4">
            <Field label="Campaign name" hint="For your team; recipients never see it.">
              {(fid) => <input id={fid} required readOnly={!canEdit} className={inputClass} value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />}
            </Field>
            <Field label="Subject line" hint="Merge tags work here too: {{first_name | there}}">
              {(fid) => <input id={fid} readOnly={!canEdit} className={inputClass} value={form.subject} onChange={(e) => setForm({ ...form, subject: e.target.value })} />}
            </Field>
            <Field label="Preview text" hint="Shown next to the subject in most inboxes.">
              {(fid) => <input id={fid} readOnly={!canEdit} className={inputClass} value={form.preheader} onChange={(e) => setForm({ ...form, preheader: e.target.value })} />}
            </Field>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Topic" hint="Only people subscribed to it get the email.">
                {(fid) => (
                  <select id={fid} disabled={!canEdit} className={inputClass} value={topicID} onChange={(e) => setForm({ ...form, topic_id: e.target.value })}>
                    {topics.data?.map((t) => (
                      <option key={t.id} value={t.id}>
                        {t.name} ({fmt(t.subscribers)})
                      </option>
                    ))}
                  </select>
                )}
              </Field>
              <Field label="Template" hint="Templates with errors can't be sent.">
                {(fid) => (
                  <select
                    id={fid}
                    disabled={!canEdit}
                    className={inputClass}
                    value={form.template_id ?? ""}
                    onChange={(e) => setForm({ ...form, template_id: e.target.value || null })}
                  >
                    <option value="">Choose a template…</option>
                    {usable.map((t) => (
                      <option key={t.id} value={t.id}>
                        {t.name}
                      </option>
                    ))}
                  </select>
                )}
              </Field>
            </div>
            <label className="flex items-start gap-2 text-sm">
              <input
                type="checkbox"
                className="mt-1"
                disabled={!canEdit}
                checked={form.include_at_risk}
                onChange={(e) => setForm({ ...form, include_at_risk: e.target.checked })}
              />
              <span>
                Include people whose interest is fading (3–6 months without a click)
                <span className="block text-xs text-zinc-500">They complain more often. Including them needs a second person's approval.</span>
              </span>
            </label>
            <div className="flex flex-wrap items-center justify-end gap-3">
              <ErrorNote error={save.error} />
              {canEdit && (
                <Button type="submit" variant={c ? "secondary" : "primary"} disabled={!form.name.trim() || !topicID || save.isPending || !dirty}>
                  {save.isPending ? "Saving…" : c ? "Save" : "Create campaign"}
                </Button>
              )}
              {c && canSend && c.state === "draft" && (
                <Link to="review">
                  <Button variant="primary" disabled={dirty}>
                    <Send className="size-4" /> Review and schedule
                  </Button>
                </Link>
              )}
            </div>
          </div>
        </Card>
        <div className="space-y-4">
          <Card title="Who gets it" description={dirty && c ? "Save to update the counts." : "Counted now; fixed when it's scheduled."}>
            <div className="border-t border-zinc-100">
              {data?.audience ? <AudienceBreakdown {...summaryOf(data.audience)} /> : <p className="px-5 py-4 text-sm text-zinc-500">Create the campaign to see its audience.</p>}
            </div>
          </Card>
          {c && <TestCard slug={slug} campaign={c} />}
        </div>
      </form>
    </div>
  );
}

function TestCard({ slug, campaign }: { slug: string; campaign: Campaign }) {
  const { canEdit } = useBrandAccess(slug);
  const seeds = useSeeds(slug);
  const test = useTestSend(slug, campaign.id);
  return (
    <Card title="Send a test" description="Goes to the seed list only, marked [Test].">
      <div className="space-y-2 border-t border-zinc-100 px-5 py-4 text-sm">
        {seeds.data?.length ? (
          <p className="text-zinc-600">{seeds.data.map((s) => s.email).join(", ")}</p>
        ) : (
          <p className="text-zinc-500">The seed list is empty; a brand admin can add your team's inboxes on the Campaigns page.</p>
        )}
        {canEdit && (
          <Button type="button" size="sm" disabled={!seeds.data?.length || !campaign.template_id || test.isPending} onClick={() => test.mutate([])}>
            {test.isPending ? "Sending…" : "Send test"}
          </Button>
        )}
        {test.isSuccess && <p className="text-green-700">Sent to {test.data.sent} {test.data.sent === 1 ? "address" : "addresses"}.</p>}
        <ErrorNote error={test.error} />
      </div>
    </Card>
  );
}

// CampaignStatus: a scheduled or running campaign, its progress and controls.
function CampaignStatus({ slug, data }: { slug: string; data: CampaignResponse }) {
  const c = data.campaign;
  const { canSend } = useBrandAccess(slug);
  const cancel = useCampaignAction(slug, c.id, "cancel");
  const pause = useCampaignAction(slug, c.id, "pause");
  const resume = useCampaignAction(slug, c.id, "resume");
  const p = data.progress ?? { test: {}, rest: {} };
  const count = (k: "sent" | "failed" | "skipped" | "queued") => (p.test[k] ?? 0) + (p.rest[k] ?? 0);
  const running = ["test_slice", "holding", "sending"].includes(c.state);
  const startsIn = c.start_at ? Math.max(0, Math.ceil((new Date(c.start_at).getTime() - Date.now()) / 60000)) : 0;

  return (
    <div className="max-w-5xl space-y-4">
      <Back slug={slug} />
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">{c.name}</h1>
          <p className="mt-1 text-sm text-zinc-600">
            {c.subject} · from <span className="font-mono text-xs">{c.from_header}</span>
          </p>
        </div>
        <StateBadge state={c.state} />
      </div>

      {c.state === "scheduled" && (
        <p role="status" className="rounded-lg border border-blue-200 bg-blue-50 px-4 py-3 text-sm text-blue-900">
          Starts {startsIn > 0 ? `in ${startsIn} minute${startsIn === 1 ? "" : "s"}` : "now"}. Until then you can still cancel it.
        </p>
      )}
      {c.state === "holding" && c.hold_until && (
        <p role="status" className="rounded-lg border border-indigo-200 bg-indigo-50 px-4 py-3 text-sm text-indigo-900">
          The test slice is out. Bounces and complaints are watched until {new Date(c.hold_until).toLocaleTimeString()}, then the rest goes.
        </p>
      )}
      {c.state === "paused" && <p role="alert" className="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-900">Paused: {c.pause_reason}</p>}
      {c.waiting_reason && c.state === "sending" && (
        <p role="status" className="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-900">{c.waiting_reason}</p>
      )}

      <div className="grid gap-4 lg:grid-cols-[1fr_320px]">
        <Card title="Progress">
          <dl className="grid grid-cols-2 gap-4 border-t border-zinc-100 px-5 py-4 sm:grid-cols-4">
            {(
              [
                ["Sent", count("sent")],
                ["Waiting", count("queued")],
                ["Skipped", count("skipped")],
                ["Failed", count("failed")],
              ] as const
            ).map(([label, n]) => (
              <div key={label}>
                <dt className="text-xs text-zinc-500">{label}</dt>
                <dd className="text-2xl font-semibold tabular-nums">{fmt(n)}</dd>
              </div>
            ))}
          </dl>
          <p className="border-t border-zinc-100 px-5 py-3 text-xs text-zinc-500">
            Test slice {c.test_slice_pct ?? 0}% ({fmt((p.test.sent ?? 0) + (p.test.queued ?? 0) + (p.test.failed ?? 0) + (p.test.skipped ?? 0))}), then a{" "}
            {c.hold_minutes ?? 0}-minute watch. Skipped: people who unsubscribed or bounced since it was scheduled.
          </p>
          {canSend && (
            <div className="flex flex-wrap justify-end gap-2 border-t border-zinc-100 px-5 py-3">
              <ErrorNote error={cancel.error ?? pause.error ?? resume.error} />
              {running && (
                <Button size="sm" onClick={() => pause.mutate(undefined)} disabled={pause.isPending}>
                  Pause
                </Button>
              )}
              {c.state === "paused" && (
                <Button size="sm" variant="primary" onClick={() => resume.mutate(undefined)} disabled={resume.isPending}>
                  Resume
                </Button>
              )}
              {!["done", "cancelled"].includes(c.state) && (
                <Button
                  size="sm"
                  variant="danger"
                  disabled={cancel.isPending}
                  onClick={() => window.confirm("Cancel this campaign? Emails already sent stay sent; nobody else gets it.") && cancel.mutate(undefined)}
                >
                  Cancel campaign
                </Button>
              )}
            </div>
          )}
        </Card>
        <Card title="Who gets it" description="Fixed when it was scheduled.">
          <div className="border-t border-zinc-100">{data.breakdown && <AudienceBreakdown {...breakdownOf(data.breakdown)} />}</div>
        </Card>
      </div>
      {c.state !== "scheduled" && c.state !== "cancelled" && <LiveMonitor slug={slug} id={c.id} live={running} />}
    </div>
  );
}
