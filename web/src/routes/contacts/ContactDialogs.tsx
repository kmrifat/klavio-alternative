import { useState } from "react";
import { useAddContact, useContact, useSuppress, useTopics, useUnsubscribe, type NewContact } from "../../api/contacts";
import type { BrandRole } from "../../api/types";
import { Badge, Button, Dialog, ErrorNote, Field, Spinner, inputClass } from "../../components/ui";
import { consentBasisLabel, engagementBadge, shortDate, suppressionLabel } from "../../lib/format";

function TopicChecks({ slug, value, onChange }: { slug: string; value: string[]; onChange: (v: string[]) => void }) {
  const topics = useTopics(slug).data ?? [];
  return (
    <fieldset className="space-y-1.5">
      <legend className="text-sm font-medium text-zinc-800">Topics they agreed to</legend>
      {topics.map((t) => (
        <label key={t.id} className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={value.includes(t.id)}
            onChange={(e) => onChange(e.target.checked ? [...value, t.id] : value.filter((id) => id !== t.id))}
          />
          {t.name}
        </label>
      ))}
    </fieldset>
  );
}

const empty: NewContact = {
  email: "",
  first_name: "",
  last_name: "",
  tags: [],
  topic_ids: [],
  consent: { basis: "single_opt_in", source: "", consented_at: "" },
};

export function AddContactDialog({ slug, open, onClose }: { slug: string; open: boolean; onClose: () => void }) {
  const add = useAddContact(slug);
  const [c, setC] = useState<NewContact>(empty);
  const [tags, setTags] = useState("");
  const close = () => {
    setC(empty);
    setTags("");
    add.reset();
    onClose();
  };
  return (
    <Dialog open={open} title="Add contact" onClose={close}>
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          add.mutate({ ...c, tags: tags.split(",").map((t) => t.trim()).filter(Boolean) }, { onSuccess: close });
        }}
      >
        <Field label="Email">
          {(id) => <input id={id} type="email" required className={inputClass} value={c.email} onChange={(e) => setC({ ...c, email: e.target.value })} />}
        </Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label="First name">
            {(id) => <input id={id} className={inputClass} value={c.first_name} onChange={(e) => setC({ ...c, first_name: e.target.value })} />}
          </Field>
          <Field label="Last name">
            {(id) => <input id={id} className={inputClass} value={c.last_name} onChange={(e) => setC({ ...c, last_name: e.target.value })} />}
          </Field>
        </div>
        <Field label="How did they agree?">
          {(id) => (
            <select id={id} className={inputClass} value={c.consent.basis} onChange={(e) => setC({ ...c, consent: { ...c.consent, basis: e.target.value } })}>
              <option value="single_opt_in">They opted in (single opt-in)</option>
              <option value="double_opt_in">They opted in and confirmed by email (double opt-in)</option>
              <option value="soft_opt_in">Existing customer who could opt out (soft opt-in)</option>
            </select>
          )}
        </Field>
        <Field label="Where and how" hint="Kept as proof of consent, e.g. “Signed up at the shop counter, Oct 3”.">
          {(id) => (
            <input id={id} required className={inputClass} value={c.consent.source} onChange={(e) => setC({ ...c, consent: { ...c.consent, source: e.target.value } })} />
          )}
        </Field>
        <Field label="When (optional)" hint="Leave empty for today.">
          {(id) => (
            <input id={id} type="date" className={inputClass} value={c.consent.consented_at} onChange={(e) => setC({ ...c, consent: { ...c.consent, consented_at: e.target.value } })} />
          )}
        </Field>
        <TopicChecks slug={slug} value={c.topic_ids} onChange={(topic_ids) => setC({ ...c, topic_ids })} />
        <Field label="Tags (optional)" hint="Comma-separated.">
          {(id) => <input id={id} className={inputClass} value={tags} onChange={(e) => setTags(e.target.value)} />}
        </Field>
        <ErrorNote error={add.error} />
        <div className="flex justify-end gap-2">
          <Button onClick={close}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={add.isPending}>
            {add.isPending ? "Adding…" : "Add contact"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

const kindLabel: Record<string, string> = {
  opt_in: "Opted in",
  confirmed: "Confirmed by email",
  import_attested: "Imported with signed attestation",
  unsubscribed: "Unsubscribed",
  resubscribed: "Subscribed again",
};

export function ContactDialog({ slug, id, role, onClose }: { slug: string; id: string; role: BrandRole | ""; onClose: () => void }) {
  const detail = useContact(slug, id);
  const topics = useTopics(slug).data ?? [];
  const unsubscribe = useUnsubscribe(slug);
  const suppress = useSuppress(slug);
  const [action, setAction] = useState<"unsubscribe" | "suppress" | null>(null);
  const [note, setNote] = useState("");

  const c = detail.data?.contact;
  const title = c ? [c.first_name, c.last_name].filter(Boolean).join(" ") || c.email : "Contact";
  const blocked = !!c?.suppression_reason;

  const run = () => {
    const m = action === "suppress" ? suppress : unsubscribe;
    m.mutate({ id, note }, { onSuccess: () => setAction(null) });
  };

  return (
    <Dialog open title={title} onClose={onClose}>
      {detail.isPending ? (
        <Spinner />
      ) : detail.isError ? (
        <ErrorNote error={detail.error} />
      ) : (
        c && (
          <div className="space-y-4 text-sm">
            <div className="flex items-center justify-between">
              <span className="text-zinc-600">{c.email}</span>
              <Badge tone={engagementBadge(c).tone} dot>
                {engagementBadge(c).label}
              </Badge>
            </div>
            {blocked && (
              <p className="rounded-md bg-zinc-100 px-3 py-2 text-zinc-700">
                Blocked for every brand: {suppressionLabel[c.suppression_reason!] ?? c.suppression_reason}.
              </p>
            )}
            <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5">
              <dt className="text-zinc-500">Consent</dt>
              <dd>{consentBasisLabel[c.consent_basis]}</dd>
              <dt className="text-zinc-500">Topics</dt>
              <dd>{detail.data.topic_ids.map((t) => topics.find((x) => x.id === t)?.name).filter(Boolean).join(", ") || "none"}</dd>
              <dt className="text-zinc-500">Last click</dt>
              <dd>{c.last_click_at ? shortDate(c.last_click_at) : "never"}</dd>
              {c.tags.length > 0 && (
                <>
                  <dt className="text-zinc-500">Tags</dt>
                  <dd>{c.tags.join(", ")}</dd>
                </>
              )}
            </dl>

            <div>
              <h3 className="mb-2 text-xs font-medium tracking-wide text-zinc-500 uppercase">Consent history</h3>
              <ol className="space-y-2 border-l border-zinc-200 pl-3">
                {detail.data.consent_history.map((r) => (
                  <li key={r.id}>
                    <p className="font-medium">
                      {kindLabel[r.kind] ?? r.kind} <span className="font-normal text-zinc-500">· {shortDate(r.occurred_at)}</span>
                    </p>
                    {r.source && <p className="text-xs text-zinc-600">{r.source}</p>}
                    {r.wording && <p className="text-xs text-zinc-500">“{r.wording}”</p>}
                  </li>
                ))}
              </ol>
            </div>

            {action ? (
              <div className="space-y-2 rounded-lg border border-zinc-200 p-3">
                <p className="font-medium">
                  {action === "suppress" ? "Block this address for every brand, permanently?" : "Unsubscribe from all topics?"}
                </p>
                <input
                  aria-label="Reason"
                  required={action === "suppress"}
                  placeholder={action === "suppress" ? "Why (required), e.g. asked never to be emailed" : "Note (optional), e.g. asked by phone"}
                  className={inputClass}
                  value={note}
                  onChange={(e) => setNote(e.target.value)}
                />
                <ErrorNote error={unsubscribe.error ?? suppress.error} />
                <div className="flex justify-end gap-2">
                  <Button size="sm" onClick={() => setAction(null)}>
                    Back
                  </Button>
                  <Button size="sm" variant="danger" disabled={unsubscribe.isPending || suppress.isPending} onClick={run}>
                    {action === "suppress" ? "Suppress everywhere" : "Unsubscribe"}
                  </Button>
                </div>
              </div>
            ) : (
              !blocked && (
                <div className="flex justify-end gap-2 border-t border-zinc-100 pt-3">
                  {c.status !== "unsubscribed" && (
                    <Button size="sm" onClick={() => setAction("unsubscribe")}>
                      Unsubscribe
                    </Button>
                  )}
                  {role === "brand_admin" && (
                    <Button size="sm" variant="ghost" className="text-red-700" onClick={() => setAction("suppress")}>
                      Suppress everywhere
                    </Button>
                  )}
                </div>
              )
            )}
          </div>
        )
      )}
    </Dialog>
  );
}
