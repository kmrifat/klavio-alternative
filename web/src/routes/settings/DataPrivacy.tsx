import { useState, type FormEvent } from "react";
import { CheckCircle2, Info, Search, ShieldOff } from "lucide-react";
import { useErase, useErasures, useLookupPerson, type Erasure, type PersonData } from "../../api/privacy";
import { useMe } from "../../api/hooks";
import { Badge, Button, Card, ErrorNote, Field, Spinner, inputClass } from "../../components/ui";
import { shortDate, timeAgo } from "../../lib/format";

const suppressionLabel: Record<string, string> = {
  hard_bounce: "their address bounced",
  soft_bounce: "their address kept bouncing",
  complaint: "they marked an email as spam",
  manual: "an admin blocked them",
  erasure: "they were erased",
};

const plural = (n: number, one: string, many = one + "s") => `${n.toLocaleString()} ${n === 1 ? one : many}`;
const norm = (s: string) => s.trim().toLowerCase();

// GDPR erasure: when someone asks you to delete their data, remove it from
// every brand. Mirrors POST /admin/v1/erasures.
export function DataPrivacy() {
  const me = useMe().data!;
  const canErase = me.user.company_role !== "member";
  const erasures = useErasures(canErase);
  const [done, setDone] = useState<Erasure | null>(null);

  if (!canErase) {
    return <p className="text-sm text-zinc-600">Only owners and approvers can erase people. Ask one of them if someone wants their data deleted.</p>;
  }
  return (
    <div className="space-y-4">
      <p className="flex gap-2 rounded-lg border border-sky-200 bg-sky-50 px-4 py-3 text-sm text-sky-900">
        <Info className="mt-0.5 size-4 shrink-0" />
        <span>
          <strong>When someone asks you to delete their data</strong> (their "right to erasure" under GDPR), erase them here. It covers
          every brand, can't be undone, and should be done within a month of the request.
        </span>
      </p>
      {done && <Done erasure={done} />}
      <EraseCard onErased={setDone} />
      <Card title="Erasures done" description="Proof the requests were carried out. Addresses are shown masked.">
        {erasures.isPending ? (
          <div className="border-t border-zinc-200 px-5 py-4">
            <Spinner />
          </div>
        ) : erasures.isError ? (
          <div className="border-t border-zinc-200 px-5 py-4">
            <ErrorNote error={erasures.error} />
          </div>
        ) : erasures.data.length === 0 ? (
          <p className="border-t border-zinc-200 px-5 py-4 text-sm text-zinc-600">Nobody has been erased yet.</p>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="border-y border-zinc-200 bg-zinc-50 text-left text-[11px] tracking-wide text-zinc-500 uppercase">
                <th className="px-5 py-2 font-medium">Person</th>
                <th className="px-3 py-2 font-medium">Request</th>
                <th className="px-3 py-2 font-medium">Removed</th>
                <th className="px-5 py-2 text-right font-medium">Erased</th>
              </tr>
            </thead>
            <tbody>
              {erasures.data.map((e) => (
                <tr key={e.id} className="border-b border-zinc-100 last:border-0">
                  <td className="px-5 py-2.5 font-mono text-xs [overflow-wrap:anywhere]">{e.contact_ref}</td>
                  <td className="px-3 py-2.5">{e.reference}</td>
                  <td className="px-3 py-2.5 text-zinc-600">
                    {e.summary.contacts_deleted === 0 ? "No contacts; address blocked" : plural(e.summary.contacts_deleted, "contact")}
                  </td>
                  <td className="px-5 py-2.5 text-right text-zinc-600">
                    <p title={e.erased_at}>{shortDate(e.erased_at)}</p>
                    <p className="text-xs text-zinc-500">by {e.erased_by_name}</p>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>
    </div>
  );
}

function EraseCard({ onErased }: { onErased: (e: Erasure) => void }) {
  const lookup = useLookupPerson();
  const erase = useErase();
  const [email, setEmail] = useState("");
  const [reference, setReference] = useState("");
  const [confirm, setConfirm] = useState("");

  const find = (ev: FormEvent) => {
    ev.preventDefault();
    erase.reset();
    if (email.trim()) lookup.mutate(email.trim());
  };
  const person = lookup.data;
  const looked = person && lookup.variables && norm(lookup.variables) === norm(email);
  const namesPerson = !!email.trim() && reference.toLowerCase().includes(norm(email));
  const ready = looked && reference.trim() !== "" && !namesPerson && norm(confirm) === norm(email);

  const submit = () =>
    erase.mutate(
      { email: email.trim(), confirm_email: confirm.trim(), reference: reference.trim() },
      {
        onSuccess: (e) => {
          onErased(e);
          setEmail("");
          setReference("");
          setConfirm("");
          lookup.reset();
        },
      },
    );

  return (
    <Card title="Erase a person" description="Look up what's held about an address, then erase it">
      <div className="space-y-4 border-t border-zinc-200 px-5 py-4">
        <form onSubmit={find} className="flex items-end gap-2">
          <div className="flex-1">
            <Field label="Email address">
              {(id) => (
                <input
                  id={id}
                  type="email"
                  autoComplete="off"
                  className={inputClass}
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  placeholder="person@example.com"
                />
              )}
            </Field>
          </div>
          <Button type="submit" disabled={!email.trim() || lookup.isPending}>
            <Search className="size-4" />
            Look up
          </Button>
        </form>
        <ErrorNote error={lookup.error} />

        {looked && person && (
          <>
            <Findings p={person} />
            <Field label="Where the request came from" hint="Kept for good as proof, so leave the address out." error={namesPerson ? "Leave the email address out of this." : undefined}>
              {(id) => (
                <input
                  id={id}
                  className={inputClass}
                  value={reference}
                  maxLength={500}
                  onChange={(e) => setReference(e.target.value)}
                  placeholder="Email to privacy@, 3 Oct"
                />
              )}
            </Field>
            <Field label="Type the address again to confirm">
              {(id) => (
                <input id={id} type="email" autoComplete="off" className={inputClass} value={confirm} onChange={(e) => setConfirm(e.target.value)} />
              )}
            </Field>
            <ErrorNote error={erase.error} />
            <div className="flex items-center justify-end gap-3">
              <p className="text-xs text-zinc-500">You'll confirm with your passkey. This can't be undone.</p>
              <Button variant="danger" disabled={!ready || erase.isPending} onClick={submit}>
                <ShieldOff className="size-4" />
                {erase.isPending ? "Erasing…" : "Erase permanently"}
              </Button>
            </div>
          </>
        )}
      </div>
    </Card>
  );
}

function Findings({ p }: { p: PersonData }) {
  const last = p.erasures[0];
  return (
    <div className="space-y-3 rounded-lg border border-zinc-200 bg-zinc-50 px-4 py-3 text-sm">
      {p.contacts.length === 0 ? (
        <p>No brand has this address as a contact.</p>
      ) : (
        <div>
          <p className="font-medium">A contact on {plural(p.contacts.length, "brand")}:</p>
          <ul className="mt-1.5 flex flex-wrap gap-2">
            {p.contacts.map((c) => (
              <li key={c.brand_slug} className="flex items-center gap-1.5 rounded-md border border-zinc-200 bg-white px-2 py-1 text-xs">
                <span className="size-2 rounded-full" style={{ background: c.brand_color }} />
                {c.brand_name}
                <span className="text-zinc-500">· {c.status.replace(/_/g, " ")}</span>
              </li>
            ))}
          </ul>
        </div>
      )}
      {(p.suppression || last) && (
        <div className="flex flex-wrap gap-2">
          {p.suppression && <Badge tone="zinc">Blocked: {suppressionLabel[p.suppression] ?? p.suppression}</Badge>}
          {last && (
            <Badge tone="amber">
              Already erased {timeAgo(last.erased_at)} by {last.erased_by_name}
            </Badge>
          )}
        </div>
      )}
      <div>
        <p className="font-medium">Erasing will:</p>
        <ul className="mt-1 list-disc space-y-0.5 pl-5 text-zinc-700">
          {p.contacts.length > 0 && <li>Delete the contact from {plural(p.contacts.length, "brand")}, with their topics and tags</li>}
          <li>Block the address for good, so no import, sign-up form or admin can add it again</li>
          {p.clicks + p.opens > 0 && (
            <li>
              Remove the IP address and browser from {plural(p.clicks, "click")} and {plural(p.opens, "open")}. Campaign totals stay the same.
            </li>
          )}
          {p.events > 0 && <li>Empty {plural(p.events, "report")} from Amazon about their email (deliveries, bounces, spam complaints)</li>}
          {p.audit_entries > 0 && (
            <li>
              Replace the address in {plural(p.audit_entries, "audit log entry", "audit log entries")} with{" "}
              <span className="font-mono text-xs">{p.contact_ref}</span>
            </li>
          )}
          <li>Take them out of import previews, stored sign-up replies and export files</li>
        </ul>
      </div>
      {p.consent_records > 0 && (
        <p className="text-zinc-600">
          <strong>Kept:</strong> {plural(p.consent_records, "consent record")}, filed under a scrambled code, as proof they agreed if it's
          ever disputed.
        </p>
      )}
    </div>
  );
}

function Done({ erasure: e }: { erasure: Erasure }) {
  const s = e.summary;
  return (
    <div role="status" className="flex gap-2 rounded-lg border border-green-200 bg-green-50 px-4 py-3 text-sm text-green-900">
      <CheckCircle2 className="mt-0.5 size-4 shrink-0" />
      <div>
        <p>
          <strong>Erased {e.contact_ref}.</strong>{" "}
          {s.contacts_deleted > 0 ? `Deleted from ${s.brands.join(", ")}.` : "No brand had them as a contact."} The address is blocked for good.
        </p>
        <p className="mt-0.5 text-xs text-green-800">
          Blanked {plural(s.clicks_blanked + s.opens_blanked, "click or open", "clicks and opens")}, {plural(s.events_blanked, "Amazon report")} and{" "}
          {plural(s.audit_entries_redacted, "audit entry", "audit entries")}. Kept {plural(s.consent_records_kept, "consent record")}.
        </p>
      </div>
    </div>
  );
}
