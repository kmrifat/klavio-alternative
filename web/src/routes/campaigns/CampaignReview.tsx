import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { AlertTriangle, ArrowLeft, ShieldCheck, XCircle } from "lucide-react";
import { ApiError } from "../../api/client";
import { useCampaign, useReview, useSchedule } from "../../api/campaigns";
import { Button, Card, ErrorNote, Field, inputClass, Spinner } from "../../components/ui";
import { useBrandAccess } from "../templates/parts";
import { AudienceBreakdown, fmt, summaryOf } from "./parts";

// S6: the last look before sending. The sender types the number of people
// it goes to, so nobody schedules a send without reading it.
export function CampaignReview() {
  const { brand: slug, id } = useParams() as { brand: string; id: string };
  const navigate = useNavigate();
  const { canSend } = useBrandAccess(slug);
  const c = useCampaign(slug, id);
  const rv = useReview(slug, id);
  const schedule = useSchedule(slug, id);
  const [typed, setTyped] = useState("");
  const [later, setLater] = useState("");

  if (c.isPending || rv.isPending) return <Spinner />;
  if (c.isError || rv.isError) return <ErrorNote error={c.error ?? rv.error} />;
  const r = rv.data;
  const campaign = c.data.campaign;
  const needsApproval = r.approval_reasons.length > 0;
  const blocked = r.problems.length > 0 || campaign.state !== "draft";
  const matches = typed.replace(/[\s,.  ]/g, "") === r.confirm_text;
  const conflict = schedule.error instanceof ApiError && schedule.error.code === "confirm_mismatch";

  return (
    <div className="max-w-3xl space-y-4">
      <Link to={`/b/${slug}/campaigns/${id}`} className="inline-flex items-center gap-1 text-sm text-zinc-600 hover:text-zinc-900">
        <ArrowLeft className="size-4" /> Back to the draft
      </Link>
      <h1 className="text-2xl font-semibold tracking-tight">Ready to send “{campaign.name}”?</h1>

      {r.problems.length > 0 && (
        <Card title="Fix these first">
          <ul className="space-y-1.5 border-t border-zinc-100 px-5 py-4 text-sm">
            {r.problems.map((p) => (
              <li key={p} className="flex gap-2">
                <XCircle className="mt-0.5 size-4 shrink-0 text-red-600" /> {p}
              </li>
            ))}
          </ul>
        </Card>
      )}

      {needsApproval && (
        <p role="note" className="flex gap-2 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-900">
          <AlertTriangle className="mt-0.5 size-4 shrink-0" />
          <span>
            This send needs a second person's approval, because it {r.approval_reasons.map((a) => a.text).join(", and it ")}. Scheduling sends them a
            request; it goes out once they approve.
          </span>
        </p>
      )}

      <Card>
        <dl className="grid gap-x-6 gap-y-3 px-5 py-4 text-sm sm:grid-cols-[auto_1fr]">
          <dt className="text-zinc-500">From</dt>
          <dd className="font-mono text-xs leading-5">{r.from_header}</dd>
          <dt className="text-zinc-500">Subject</dt>
          <dd>{campaign.subject || <em className="text-zinc-400">none</em>}</dd>
          <dt className="text-zinc-500">Safety</dt>
          <dd className="flex gap-2">
            <ShieldCheck className="mt-0.5 size-4 shrink-0 text-green-600" />
            <span>
              First {r.test_slice_pct}% only, then a {r.hold_minutes}-minute watch for bounces and complaints. Nothing goes out for{" "}
              {r.cancel_window_minutes} minutes after you confirm, so you can still cancel. Quiet hours (21:00–08:00 in each person's timezone) are kept.
            </span>
          </dd>
        </dl>
        <div className="border-t border-zinc-100">
          <AudienceBreakdown {...summaryOf(r.audience)} />
        </div>
      </Card>

      {canSend && !blocked && (
        <Card title="Confirm">
          <form
            className="space-y-4 border-t border-zinc-100 px-5 py-4"
            onSubmit={(e) => {
              e.preventDefault();
              schedule.mutate(
                { version: campaign.version, confirm: typed, ...(later ? { scheduled_at: new Date(later).toISOString() } : {}) },
                {
                  onSuccess: () => navigate(`/b/${slug}/campaigns/${id}`),
                  onError: (err) => err instanceof ApiError && err.code === "confirm_mismatch" && void rv.refetch(),
                },
              );
            }}
          >
            <Field label="Start" hint="Empty: as soon as the cancel window ends.">
              {(fid) => <input id={fid} type="datetime-local" className={inputClass} value={later} onChange={(e) => setLater(e.target.value)} />}
            </Field>
            <Field label={`Type ${fmt(Number(r.confirm_text))} to confirm the number of people`}>
              {(fid) => (
                <input id={fid} inputMode="numeric" autoComplete="off" className={inputClass} value={typed} onChange={(e) => setTyped(e.target.value)} />
              )}
            </Field>
            {conflict ? (
              <p role="alert" className="rounded-md bg-amber-50 px-3 py-2 text-sm text-amber-900">
                {schedule.error?.message}
              </p>
            ) : (
              <ErrorNote error={schedule.error} />
            )}
            <div className="flex justify-end">
              <Button type="submit" variant="primary" disabled={!matches || schedule.isPending}>
                {schedule.isPending ? "Scheduling…" : needsApproval ? "Ask for approval" : `Send to ${fmt(Number(r.confirm_text))} people`}
              </Button>
            </div>
          </form>
        </Card>
      )}
    </div>
  );
}
