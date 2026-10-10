import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useMutation } from "@tanstack/react-query";
import { AlertTriangle, Check, CheckCircle2, FileUp, XCircle } from "lucide-react";
import { uploadImport, useImport, useImportActions, useTopics } from "../../api/contacts";
import type { CheckResult, ImportConsent, ImportMapping, ImportView, Outcome } from "../../api/types";
import { Badge, Button, Card, ErrorNote, Field, PageHeader, Spinner, inputClass } from "../../components/ui";
import { outcomeInfo } from "../../lib/format";

const steps = ["Upload file", "Match columns", "Consent & safety check", "Review & import"];

export function ImportWizard() {
  const slug = useParams().brand!;
  const id = useParams().id;
  const view = useImport(slug, id);

  return (
    <div className="max-w-6xl">
      <PageHeader
        title="Import contacts"
        description="Imports are checked before anyone can be emailed. Bought, rented or scraped lists are not allowed."
        action={
          <Link to={`/b/${slug}/contacts`} className="text-sm text-zinc-600 hover:text-zinc-900">
            {view.data?.import.status === "done" ? "Back to contacts" : "Cancel import"}
          </Link>
        }
      />
      {!id ? (
        <>
          <Stepper current={0} />
          <UploadStep slug={slug} />
        </>
      ) : view.isPending ? (
        <Spinner />
      ) : view.isError ? (
        <ErrorNote error={view.error} />
      ) : (
        <ImportFlow slug={slug} view={view.data} />
      )}
    </div>
  );
}

function Stepper({ current }: { current: number }) {
  return (
    <ol className="mb-6 flex items-center gap-3">
      {steps.map((s, i) => (
        <li key={s} className="flex flex-1 items-center gap-2 last:flex-none" aria-current={i === current ? "step" : undefined}>
          <span
            className={`grid size-6 shrink-0 place-items-center rounded-full text-xs font-semibold ${
              i < current ? "bg-green-600 text-white" : i === current ? "bg-accent text-white" : "border border-zinc-300 text-zinc-500"
            }`}
          >
            {i < current ? <Check className="size-3.5" /> : i + 1}
          </span>
          <span className={`text-sm whitespace-nowrap ${i === current ? "font-medium" : "text-zinc-600"}`}>{s}</span>
          {i < steps.length - 1 && <span className="h-px flex-1 bg-zinc-200" />}
        </li>
      ))}
    </ol>
  );
}

function UploadStep({ slug }: { slug: string }) {
  const navigate = useNavigate();
  const upload = useMutation({
    mutationFn: (f: File) => uploadImport(slug, f),
    onSuccess: (v) => navigate(`/b/${slug}/contacts/import/${v.import.id}`),
  });
  return (
    <Card>
      <label className="m-5 flex cursor-pointer flex-col items-center gap-2 rounded-lg border-2 border-dashed border-zinc-300 px-6 py-12 text-center hover:border-accent">
        <FileUp className="size-8 text-zinc-400" />
        <span className="font-medium">{upload.isPending ? "Uploading…" : "Choose a CSV file"}</span>
        <span className="text-sm text-zinc-500">An export from Mailchimp, Klaviyo, Shopify or a spreadsheet saved as CSV · up to 20 MB</span>
        <input
          type="file"
          accept=".csv,text/csv"
          aria-label="CSV file"
          className="sr-only"
          disabled={upload.isPending}
          onChange={(e) => e.target.files?.[0] && upload.mutate(e.target.files[0])}
        />
      </label>
      <div className="px-5 pb-5">
        <ErrorNote error={upload.error} />
        <p className="text-xs text-zinc-500">Nothing is sent by importing. The file is deleted once the import finishes.</p>
      </div>
    </Card>
  );
}

const emptyConsent: ImportConsent = {
  source: "",
  source_detail: "",
  collected_period: "",
  wording: "",
  legal_basis: "consent",
  double_opt_in: false,
  attested: false,
};

function ImportFlow({ slug, view }: { slug: string; view: ImportView }) {
  const imp = view.import;
  const actions = useImportActions(slug, imp.id);
  const [mapping, setMapping] = useState<ImportMapping>(imp.mapping);
  const [consent, setConsent] = useState<ImportConsent>({ ...emptyConsent, ...imp.consent });
  const [topicIds, setTopicIds] = useState<string[]>(imp.topic_ids);
  const topics = useTopics(slug).data ?? [];
  const [step, setStep] = useState(imp.status === "uploaded" && !imp.check_result ? 1 : imp.status === "importing" || imp.status === "done" ? 3 : 2);

  // Default to the brand's only topic so the common case is one click less.
  useEffect(() => {
    if (topicIds.length === 0 && topics.length === 1) setTopicIds([topics[0]!.id]);
  }, [topics, topicIds.length]);

  useEffect(() => {
    if (imp.status === "importing" || imp.status === "done") setStep(3);
  }, [imp.status]);

  if (imp.status === "cancelled") {
    return <ErrorNote error={new Error("This import was cancelled. Start a new one from Contacts.")} />;
  }

  const runCheck = () =>
    actions.setup.mutate({ mapping, consent, topic_ids: topicIds }, { onSuccess: () => actions.check.mutate() });

  return (
    <>
      <Stepper current={step} />
      {imp.status === "failed" && (
        <div className="mb-4">
          <ErrorNote error={new Error(imp.error)} />
        </div>
      )}
      {step === 1 && <MappingStep view={view} mapping={mapping} setMapping={setMapping} onNext={() => setStep(2)} />}
      {step === 2 && (
        <div className="grid gap-5 lg:grid-cols-[1fr_minmax(0,26rem)]">
          <ConsentForm
            view={view}
            consent={consent}
            setConsent={setConsent}
            topics={topics}
            topicIds={topicIds}
            setTopicIds={setTopicIds}
          />
          <CheckPanel
            result={imp.status === "checking" ? null : imp.check_result}
            checking={imp.status === "checking" || actions.setup.isPending || actions.check.isPending}
            error={actions.setup.error ?? actions.check.error}
            onRun={runCheck}
            onBack={() => setStep(1)}
            onNext={() => setStep(3)}
          />
        </div>
      )}
      {step === 3 && <ReviewStep slug={slug} view={view} actions={actions} onBack={() => setStep(2)} />}
    </>
  );
}

const mappingFields: { key: keyof ImportMapping; label: string; hint: string }[] = [
  { key: "email", label: "Email", hint: "Required" },
  { key: "first_name", label: "First name", hint: "" },
  { key: "last_name", label: "Last name", hint: "" },
  { key: "consented_at", label: "Signup / confirmation date", hint: "Used to tier and quarantine" },
  { key: "last_engaged_at", label: "Last click date", hint: "Better than signup date if the old tool has it" },
  { key: "rating", label: "Engagement rating", hint: "Mailchimp MEMBER_RATING: 4–5 stars counts as recent activity" },
  { key: "tags", label: "Tags", hint: "" },
  { key: "timezone", label: "Time zone", hint: "For quiet hours" },
  { key: "country", label: "Country", hint: "" },
];

function MappingStep({ view, mapping, setMapping, onNext }: {
  view: ImportView;
  mapping: ImportMapping;
  setMapping: (m: ImportMapping) => void;
  onNext: () => void;
}) {
  const { headers } = view.import;
  const sample = (col: string) => {
    const i = headers.indexOf(col);
    return i < 0 ? [] : view.preview.map((r) => r[i] ?? "").filter(Boolean).slice(0, 2);
  };
  const noDates = !mapping.consented_at && !mapping.last_engaged_at && !mapping.rating;
  return (
    <Card title={`Match columns · ${view.import.filename}`} description={`${view.import.row_count.toLocaleString()} rows. We guessed the columns; check them.`}>
      <table className="w-full text-sm">
        <tbody>
          {mappingFields.map((f) => (
            <tr key={f.key} className="border-t border-zinc-100">
              <td className="w-56 px-5 py-2.5">
                <p className="font-medium">{f.label}</p>
                {f.hint && <p className="text-xs text-zinc-500">{f.hint}</p>}
              </td>
              <td className="px-3 py-2.5">
                <select
                  aria-label={`Column for ${f.label}`}
                  className={inputClass}
                  value={mapping[f.key]}
                  onChange={(e) => setMapping({ ...mapping, [f.key]: e.target.value })}
                >
                  <option value="">— not in this file —</option>
                  {headers.map((h) => (
                    <option key={h} value={h}>
                      {h}
                    </option>
                  ))}
                </select>
              </td>
              <td className="px-5 py-2.5 font-mono text-xs text-zinc-500">{sample(mapping[f.key]).join(" · ")}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {noDates && (
        <p className="mx-5 mt-3 flex gap-2 rounded-md bg-amber-50 px-3 py-2 text-sm text-amber-900">
          <AlertTriangle className="mt-0.5 size-4 shrink-0" /> Without a date or rating column, nobody can be shown to be active, so everyone
          will be quarantined (they'll only get one re-permission email).
        </p>
      )}
      <div className="flex justify-end px-5 py-4">
        <Button variant="primary" disabled={!mapping.email} onClick={onNext}>
          Continue
        </Button>
      </div>
    </Card>
  );
}

function ConsentForm({ view, consent, setConsent, topics, topicIds, setTopicIds }: {
  view: ImportView;
  consent: ImportConsent;
  setConsent: (c: ImportConsent) => void;
  topics: { id: string; name: string }[];
  topicIds: string[];
  setTopicIds: (ids: string[]) => void;
}) {
  const set = <K extends keyof ImportConsent>(k: K, v: ImportConsent[K]) => setConsent({ ...consent, [k]: v });
  return (
    <Card title="Where did these contacts come from?" description="Stored with every imported contact as consent proof. Imports with an unknown source are blocked.">
      <div className="space-y-4 border-t border-zinc-100 px-5 py-5">
        <Field label="Source" hint="Bought, rented, scraped and “I don't know” are not options.">
          {(id) => (
            <select id={id} className={inputClass} value={consent.source} onChange={(e) => set("source", e.target.value)}>
              <option value="">Choose…</option>
              {Object.entries(view.sources).map(([k, label]) => (
                <option key={k} value={k}>
                  {label}
                </option>
              ))}
            </select>
          )}
        </Field>
        {consent.source === "platform_migration" && (
          <Field label="Which platform?">
            {(id) => <input id={id} placeholder="e.g. Mailchimp export" className={inputClass} value={consent.source_detail} onChange={(e) => set("source_detail", e.target.value)} />}
          </Field>
        )}
        <Field label="When were they collected?">
          {(id) => <input id={id} placeholder="e.g. Mar 2023 – Sep 2026" className={inputClass} value={consent.collected_period} onChange={(e) => set("collected_period", e.target.value)} />}
        </Field>
        <Field label="Consent wording or signup page URL">
          {(id) => <input id={id} className={inputClass} value={consent.wording} onChange={(e) => set("wording", e.target.value)} />}
        </Field>
        <Field label="Legal basis">
          {(id) => (
            <select id={id} className={inputClass} value={consent.legal_basis} onChange={(e) => set("legal_basis", e.target.value as ImportConsent["legal_basis"])}>
              <option value="consent">Consent (they opted in)</option>
              <option value="soft_opt_in">Soft opt-in (existing customers who could opt out)</option>
            </select>
          )}
        </Field>
        <fieldset className="space-y-1.5">
          <legend className="text-sm font-medium text-zinc-800">Topics they agreed to</legend>
          {topics.map((t) => (
            <label key={t.id} className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={topicIds.includes(t.id)}
                onChange={(e) => setTopicIds(e.target.checked ? [...topicIds, t.id] : topicIds.filter((x) => x !== t.id))}
              />
              {t.name}
            </label>
          ))}
        </fieldset>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={consent.double_opt_in} onChange={(e) => set("double_opt_in", e.target.checked)} />
          These people confirmed by email (double opt-in)
        </label>
        <label className={`flex gap-3 rounded-lg border p-3 text-sm ${consent.attested ? "border-accent bg-indigo-50/50" : "border-zinc-200"}`}>
          <input type="checkbox" className="mt-0.5" checked={consent.attested} onChange={(e) => set("attested", e.target.checked)} />
          <span>
            {view.attestation_text}
            {consent.attested_by_email && (
              <span className="mt-1 block text-xs text-zinc-500">
                Signed by {consent.attested_by_email}
                {consent.attested_at && ` · ${new Date(consent.attested_at).toLocaleString()}`} · kept in the audit log and on every contact
              </span>
            )}
          </span>
        </label>
      </div>
    </Card>
  );
}

const resultRows: Outcome[] = ["ready", "existing", "quarantine", "suppressed", "unsubscribed", "role", "disposable", "no_mailbox", "invalid", "duplicate"];

function CheckPanel({ result, checking, error, onRun, onBack, onNext }: {
  result: CheckResult | null;
  checking: boolean;
  error: unknown;
  onRun: () => void;
  onBack: () => void;
  onNext: () => void;
}) {
  return (
    <Card title="Safety check results" description="Every row is checked: address syntax, MX records, role and throwaway addresses, and the suppression list.">
      <div className="space-y-4 border-t border-zinc-100 px-5 py-5">
        {checking ? (
          <Spinner label="Checking every address…" />
        ) : !result ? (
          <p className="text-sm text-zinc-500">Answer the questions on the left, then run the check.</p>
        ) : (
          <>
            <ul className="space-y-2.5">
              {resultRows
                .filter((o) => (result.counts[o] ?? 0) > 0)
                .map((o) => (
                  <li key={o}>
                    <div className="flex items-center justify-between gap-2 text-sm">
                      <span>{outcomeInfo[o].label}</span>
                      <span className="flex items-center gap-2">
                        <span className="font-medium tabular-nums">{(result.counts[o] ?? 0).toLocaleString()}</span>
                        <Badge tone={outcomeInfo[o].tone}>{outcomeInfo[o].badge}</Badge>
                      </span>
                    </div>
                    <div className="mt-1 h-1.5 rounded-full bg-zinc-100">
                      <div
                        className={`h-1.5 rounded-full ${{ green: "bg-green-500", blue: "bg-sky-500", zinc: "bg-zinc-400", amber: "bg-amber-500", red: "bg-red-500" }[outcomeInfo[o].tone]}`}
                        style={{ width: `${Math.max(1, ((result.counts[o] ?? 0) / Math.max(result.total, 1)) * 100)}%` }}
                      />
                    </div>
                  </li>
                ))}
            </ul>
            <Gate result={result} />
          </>
        )}
        <ErrorNote error={error} />
        <div className="flex justify-between gap-2">
          <Button onClick={onBack}>Back</Button>
          <div className="flex gap-2">
            <Button onClick={onRun} disabled={checking}>
              {result ? "Run check again" : "Run safety check"}
            </Button>
            {result?.gate.passed && !checking && (
              <Button variant="primary" onClick={onNext}>
                Continue to review
              </Button>
            )}
          </div>
        </div>
      </div>
    </Card>
  );
}

function pct(n: number) {
  return `${(n * 100).toFixed(1)}%`;
}

function Gate({ result }: { result: CheckResult }) {
  const g = result.gate;
  const items = [
    { ok: g.invalid_rate <= g.invalid_limit, text: `Invalid rate ${pct(g.invalid_rate)} — limit ${pct(g.invalid_limit)}` },
    { ok: g.role_disposable_rate <= g.role_disposable_limit, text: `Role + disposable ${pct(g.role_disposable_rate)} — limit ${pct(g.role_disposable_limit)}` },
    { ok: g.consent_recorded, text: "Consent source and attestation recorded" },
  ];
  return (
    <div className="space-y-3">
      <div className="rounded-lg bg-zinc-50 p-3">
        <p className="mb-2 text-[11px] font-medium tracking-wide text-zinc-500 uppercase">Import gate</p>
        <ul className="space-y-1 text-sm">
          {items.map((i) => (
            <li key={i.text} className="flex items-center gap-2">
              {i.ok ? <CheckCircle2 className="size-4 text-green-600" /> : <XCircle className="size-4 text-red-600" />}
              {i.text}
            </li>
          ))}
        </ul>
      </div>
      {g.passed ? (
        <p className="rounded-lg border border-green-200 bg-green-50 px-3 py-2 text-sm text-green-800">
          <strong>Passes the import gate.</strong> The first campaign to these people will be capped and start with a test slice.
        </p>
      ) : (
        <div role="alert" className="rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-800">
          <strong>Doesn't pass yet:</strong>
          <ul className="mt-1 list-disc pl-5">
            {g.problems.map((p) => (
              <li key={p}>{p}</li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

function ReviewStep({ slug, view, actions, onBack }: {
  slug: string;
  view: ImportView;
  actions: ReturnType<typeof useImportActions>;
  onBack: () => void;
}) {
  const imp = view.import;
  const c = imp.check_result?.counts ?? {};
  const r = imp.import_result;

  if (imp.status === "importing") return <Spinner label={`Importing ${imp.row_count.toLocaleString()} rows…`} />;
  if (imp.status === "done" && r) {
    return (
      <Card title="Import finished">
        <ul className="space-y-1 border-t border-zinc-100 px-5 py-4 text-sm">
          <li>
            <strong>{r.created.toLocaleString()}</strong> new contacts
          </li>
          <li>
            <strong>{r.quarantined.toLocaleString()}</strong> quarantined (re-permission email only)
          </li>
          <li>
            <strong>{r.updated.toLocaleString()}</strong> existing contacts updated (consent unchanged)
          </li>
          <li className="text-zinc-500">
            {r.left_unsubscribed + r.left_suppressed} left blocked or unsubscribed · {r.rejected} rejected
          </li>
        </ul>
        <div className="px-5 pb-5">
          <Link to={`/b/${slug}/contacts`}>
            <Button variant="primary">View contacts</Button>
          </Link>
        </div>
      </Card>
    );
  }
  const willImport = (c.ready ?? 0) + (c.quarantine ?? 0);
  return (
    <Card title="Review & import" description="Nothing is sent by importing. Quarantined people only ever get one re-permission email.">
      <ul className="space-y-1 border-t border-zinc-100 px-5 py-4 text-sm">
        <li>
          <strong>{(c.ready ?? 0).toLocaleString()}</strong> contacts will be added and can be emailed
        </li>
        <li>
          <strong>{(c.quarantine ?? 0).toLocaleString()}</strong> will be quarantined
        </li>
        <li>
          <strong>{(c.existing ?? 0).toLocaleString()}</strong> existing contacts get blank fields filled in — their consent doesn't change
        </li>
        <li className="text-zinc-500">Everyone else stays out. Unsubscribed and suppressed people are never re-subscribed by an import.</li>
      </ul>
      <div className="px-5">
        <ErrorNote error={actions.commit.error} />
      </div>
      <div className="flex justify-between px-5 py-4">
        <Button onClick={onBack}>Back</Button>
        <Button variant="primary" disabled={actions.commit.isPending} onClick={() => actions.commit.mutate()}>
          {actions.commit.isPending ? "Starting…" : `Import ${willImport.toLocaleString()} contacts`}
        </Button>
      </div>
    </Card>
  );
}
