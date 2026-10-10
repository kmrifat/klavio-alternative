import { useState } from "react";
import { Link, useParams } from "react-router";
import { ArrowLeft, CheckCircle2, Clock, OctagonPause } from "lucide-react";
import {
  brakeInfo,
  useAccountBrake,
  useRecovery,
  useRequestAccountRelease,
  useRequestResume,
  type RecoveryStep,
  type SafetyEvent,
} from "../../api/brakes";
import { useBrands, useMe } from "../../api/hooks";
import { Badge, Button, Card, ErrorNote, PageHeader, Spinner, inputClass } from "../../components/ui";
import { timeAgo } from "../../lib/format";

// S8: guided recovery for a brand its brake paused.
export function RecoveryPage() {
  const slug = useParams().brand!;
  const me = useMe().data!;
  const brand = useBrands().data?.find((b) => b.slug === slug);
  const isAdmin = me.user.company_role === "owner" || brand?.my_role === "brand_admin";
  const q = useRecovery(slug);
  const resume = useRequestResume(slug);

  if (q.isPending) return <Spinner />;
  if (q.isError) return <ErrorNote error={q.error} />;
  const d = q.data;

  return (
    <div className="max-w-3xl space-y-5">
      <Link to={`/b/${slug}/health`} className="inline-flex items-center gap-1 text-sm text-zinc-600 hover:text-zinc-900">
        <ArrowLeft className="size-4" /> Brand health
      </Link>
      <PageHeader title="Recovery" description={`${d.brand.name}: why sending stopped, and how to start again safely.`} />

      {!d.braked ? (
        <p className="flex items-center gap-2 rounded-xl border border-green-200 bg-green-50 px-4 py-3 text-sm text-green-800">
          <CheckCircle2 className="size-4" /> {d.brand.name}'s brake is off.
        </p>
      ) : (
        <>
          <section
            className={`flex gap-3 rounded-xl border p-4 text-sm ${d.paused ? "border-red-200 bg-red-50 text-red-900" : "border-amber-200 bg-amber-50 text-amber-900"}`}
            aria-label={d.paused ? "Why it stopped" : "Why the brake is on"}
          >
            <OctagonPause className="mt-0.5 size-5 shrink-0" />
            <div>
              <p className="font-semibold">
                {d.paused
                  ? `All marketing email for ${d.brand.name} is stopped.`
                  : `${d.brand.name}'s brake is on: ${brakeInfo[d.current].label.replace("Brake: ", "")}. ${brakeInfo[d.current].help}`}
              </p>
              <p className="mt-1">
                Brakes tighten on their own but never loosen on their own: an approved request moves it to {brakeInfo[d.next].label.replace("Brake: ", "").toLowerCase()}.
              </p>
              {d.event && (
                <p className="mt-1">
                  The brake moved {timeAgo(d.event.at)}: {d.event.reason}.
                </p>
              )}
              <p className="mt-1">
                Last 7 days: {d.counts.delivered.toLocaleString()} delivered, {d.counts.bounces.toLocaleString()} hard bounces,{" "}
                {d.counts.complaints.toLocaleString()} complaints.
              </p>
            </div>
          </section>

          {d.pending ? (
            <p className="flex items-center gap-2 rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-900">
              <Clock className="size-4" /> A request to resume is waiting for a second admin ({timeAgo(d.pending.requested_at)}). See{" "}
              <Link to="/approvals" className="underline">
                Approvals
              </Link>
              .
            </p>
          ) : (
            <ResumeForm
              steps={d.steps}
              stillOver={d.still_over}
              why={d.why}
              canRequest={isAdmin}
              whoCan="A brand admin"
              action={d.paused ? "Ask to resume" : "Ask to loosen the brake"}
              pending={resume.isPending}
              error={resume.error}
              done={resume.isSuccess}
              onSubmit={(steps, reason) => resume.mutate({ steps, reason })}
            />
          )}
        </>
      )}

      <EventsCard events={d.events} />
    </div>
  );
}

function ResumeForm({ steps, stillOver, why, canRequest, whoCan, action = "Ask to resume", pending, error, done, onSubmit }: {
  steps: RecoveryStep[];
  whoCan: string;
  action?: string;
  stillOver: boolean;
  why: string;
  canRequest: boolean;
  pending: boolean;
  error: unknown;
  done: boolean;
  onSubmit: (steps: string[], reason: string) => void;
}) {
  const [checked, setChecked] = useState<string[]>([]);
  const [reason, setReason] = useState("");
  if (done) {
    return (
      <p role="status" className="rounded-xl border border-green-200 bg-green-50 px-4 py-3 text-sm text-green-800">
        Request sent. Another admin has to approve it; the brand then resumes on watch.
      </p>
    );
  }
  return (
    <Card title="Recovery checklist" description="Work through each step. Resuming needs a second admin's approval, and the brand comes back on watch.">
      <ol aria-label="Recovery checklist" className="space-y-2 border-t border-zinc-200 px-5 py-4">
        {steps.map((s, i) => (
          <li key={s.id}>
            <label className="flex gap-3 text-sm">
              <input
                type="checkbox"
                className="mt-1"
                disabled={!canRequest}
                checked={checked.includes(s.id)}
                onChange={(e) => setChecked(e.target.checked ? [...checked, s.id] : checked.filter((x) => x !== s.id))}
              />
              <span>
                <span className="font-medium">
                  {i + 1}. {s.title}
                </span>
                <span className="block text-zinc-600">{s.help}</span>
              </span>
            </label>
          </li>
        ))}
      </ol>
      <div className="space-y-3 border-t border-zinc-200 px-5 py-4">
        {stillOver && (
          <p className="rounded-md bg-amber-50 px-3 py-2 text-sm text-amber-900">
            The 7-day rates still call for this level ({why}). They come down as the window moves on; you can ask once they have.
          </p>
        )}
        {canRequest ? (
          <>
            <textarea
              aria-label="What you found and fixed"
              rows={3}
              className={inputClass}
              placeholder="What caused it, and what you changed. The approver reads this."
              value={reason}
              onChange={(e) => setReason(e.target.value)}
            />
            <ErrorNote error={error} />
            <div className="flex justify-end">
              <Button
                variant="primary"
                disabled={stillOver || checked.length !== steps.length || reason.trim().length < 10 || pending}
                onClick={() => onSubmit(checked, reason)}
              >
                {pending ? "Sending…" : action}
              </Button>
            </div>
          </>
        ) : (
          <p className="text-sm text-zinc-600">{whoCan} can ask to resume.</p>
        )}
      </div>
    </Card>
  );
}

const actionTone: Record<SafetyEvent["action"], "red" | "amber" | "green"> = {
  paused: "red",
  stopped: "red",
  slowed: "amber",
  watch: "amber",
  cleared: "green",
};

export function EventsCard({ events }: { events: SafetyEvent[] }) {
  return (
    <Card title="What the brakes did" description="Every change, newest first">
      {events.length === 0 ? (
        <p className="border-t border-zinc-200 px-5 py-4 text-sm text-zinc-500">Nothing yet.</p>
      ) : (
        <ul className="divide-y divide-zinc-100 border-t border-zinc-200">
          {events.map((e) => (
            <li key={e.id} className="flex items-start gap-3 px-5 py-2.5 text-sm">
              <Badge tone={actionTone[e.action]}>{e.scope === "account" ? `account ${e.action}` : e.action}</Badge>
              <span className="flex-1">{e.reason}</span>
              <span className="text-xs whitespace-nowrap text-zinc-500">{timeAgo(e.at)}</span>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

const accountSteps: RecoveryStep[] = [
  { id: "cause", title: "Find the brand and campaign behind it", help: "Check each brand's safety events and complaint rates." },
  { id: "brands", title: "Pause or fix the brands involved", help: "A brand with high complaints should stay paused until its recovery." },
  { id: "amazon", title: "Check the AWS console", help: "If Amazon paused the account, follow its notice; resuming here doesn't lift Amazon's own pause." },
];

// The account brake: every brand stopped (owners resume it with a second admin).
export function AccountBrakePage() {
  const me = useMe().data!;
  const q = useAccountBrake();
  const release = useRequestAccountRelease();
  if (q.isPending) return <Spinner />;
  if (q.isError) return <ErrorNote error={q.error} />;
  const d = q.data;
  return (
    <div className="max-w-3xl space-y-5">
      <Link to="/safety" className="inline-flex items-center gap-1 text-sm text-zinc-600 hover:text-zinc-900">
        <ArrowLeft className="size-4" /> Safety center
      </Link>
      <PageHeader title="Account brake" description="Stops every brand when complaints across brands, or Amazon, say so." />
      {!d.stopped_at ? (
        <p className="flex items-center gap-2 rounded-xl border border-green-200 bg-green-50 px-4 py-3 text-sm text-green-800">
          <CheckCircle2 className="size-4" /> Sending isn't stopped.
        </p>
      ) : (
        <>
          <p className="rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-900">
            <strong>All sending stopped {timeAgo(d.stopped_at)}.</strong> {d.reason}.
          </p>
          {d.pending ? (
            <p className="rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-900">
              A request to resume is waiting for a second admin.
            </p>
          ) : (
            <ResumeForm
              steps={accountSteps}
              stillOver={false}
              why=""
              canRequest={me.user.company_role === "owner"}
              whoCan="An owner"
              pending={release.isPending}
              error={release.error}
              done={release.isSuccess}
              onSubmit={(steps, reason) => release.mutate({ steps, reason })}
            />
          )}
        </>
      )}
    </div>
  );
}
