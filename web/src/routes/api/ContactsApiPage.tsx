import { useState } from "react";
import { useParams } from "react-router";
import { AlertTriangle, ExternalLink, KeyRound, MailCheck, Plus } from "lucide-react";
import { API_URL } from "../../api/client";
import {
  useAddWording,
  useApiCalls,
  useApiKeys,
  useCreateApiKey,
  useRepermission,
  useRetireWording,
  useRevokeApiKey,
  useSetRepermission,
  useWordings,
  type ApiKey,
  type ApiKeyState,
} from "../../api/contactsApi";
import { useBrands, useMe } from "../../api/hooks";
import { Badge, Button, Card, CopyButton, Dialog, ErrorNote, Field, PageHeader, Spinner, inputClass } from "../../components/ui";
import { shortDate, timeAgo } from "../../lib/format";

const stateBadge: Record<ApiKeyState, { label: string; tone: "green" | "amber" | "zinc" | "red" }> = {
  active: { label: "Active", tone: "green" },
  pending_approval: { label: "Waiting for approval", tone: "amber" },
  expired: { label: "Expired", tone: "zinc" },
  revoked: { label: "Revoked", tone: "red" },
};

// S14: API keys, consent wordings and double opt-in for a brand's sites and apps.
export function ContactsApiPage() {
  const slug = useParams().brand!;
  const me = useMe().data!;
  const brand = useBrands().data?.find((b) => b.slug === slug);
  const isAdmin = me.user.company_role === "owner" || brand?.my_role === "brand_admin";
  const specURL = `${API_URL}/api/v1/openapi.json`;
  const docsURL = `${API_URL}/api/v1/docs`;

  return (
    <div className="max-w-5xl space-y-5">
      <PageHeader
        title="Contacts API"
        description="Let your website and apps add people to this brand's list. Every sign-up gets a confirmation email first (double opt-in)."
        action={
          <div className="flex gap-2">
            {/* The spec as JSON, for tools that read OpenAPI. */}
            <a href={specURL} target="_blank" rel="noreferrer" title="The OpenAPI document, for tools">
              <Button variant="ghost">OpenAPI JSON</Button>
            </a>
            <a href={docsURL} target="_blank" rel="noreferrer">
              <Button>
                <ExternalLink className="size-4" /> API reference
              </Button>
            </a>
          </div>
        }
      />

      <Card title="How it works">
        <div className="space-y-2 border-t border-zinc-200 px-5 py-4 text-sm text-zinc-700">
          <p className="flex gap-2">
            <MailCheck className="mt-0.5 size-4 shrink-0 text-green-600" />
            <span>
              <strong>Double opt-in is always on.</strong> People added through the API get an email with a confirmation link and are only
              emailed after they click it. Each address gets at most 3 confirmation emails a day.
            </span>
          </p>
          <p>Call it from your server, never from a web page: anyone could read the key there.</p>
          <pre className="overflow-x-auto rounded-md bg-zinc-900 p-3 text-xs text-zinc-100">{`curl -X POST ${API_URL || location.origin}/api/v1/contacts \\
  -H "Authorization: Bearer kla_…" -H "Content-Type: application/json" \\
  -H "Idempotency-Key: 6f1c…" \\
  -d '{"email":"ann@example.com","consent":{"wording_id":"<from below>","page_url":"https://…/signup","ip":"<their IP>"}}'`}</pre>
        </div>
      </Card>

      {isAdmin && <KeysCard slug={slug} />}
      <WordingsCard slug={slug} isAdmin={isAdmin} />
      <RepermissionCard slug={slug} isAdmin={isAdmin} />
      {isAdmin && <CallsCard slug={slug} />}
    </div>
  );
}

function KeysCard({ slug }: { slug: string }) {
  const keys = useApiKeys(slug, true);
  const revoke = useRevokeApiKey(slug);
  const [creating, setCreating] = useState(false);
  return (
    <Card
      title="API keys"
      description="Each key works for this brand only, expires after at most 90 days, and needs a second admin's approval."
      action={
        <Button variant="primary" size="sm" onClick={() => setCreating(true)}>
          <Plus className="size-3.5" /> Create key
        </Button>
      }
    >
      {keys.isPending ? (
        <Spinner />
      ) : keys.isError ? (
        <div className="px-5 pb-4">
          <ErrorNote error={keys.error} />
        </div>
      ) : keys.data.keys.length === 0 ? (
        <p className="border-t border-zinc-200 px-5 py-4 text-sm text-zinc-500">No keys yet.</p>
      ) : (
        <table className="w-full border-t border-zinc-200 text-sm">
          <thead>
            <tr className="bg-zinc-50 text-left text-[11px] tracking-wide text-zinc-500 uppercase">
              <th className="px-5 py-2 font-medium">Key</th>
              <th className="px-3 py-2 font-medium">Scopes</th>
              <th className="px-3 py-2 font-medium">IPs</th>
              <th className="px-3 py-2 font-medium">Expires</th>
              <th className="px-3 py-2 font-medium">Last used</th>
              <th className="px-5 py-2" />
            </tr>
          </thead>
          <tbody>
            {keys.data.keys.map((k) => (
              <KeyRow key={k.id} k={k} onRevoke={() => confirm(`Revoke “${k.name}”? Anything using it stops working at once.`) && revoke.mutate(k.id)} />
            ))}
          </tbody>
        </table>
      )}
      <div className="px-5">
        <ErrorNote error={revoke.error} />
      </div>
      {creating && <CreateKeyDialog slug={slug} scopes={keys.data?.scopes ?? ["contacts:write", "contacts:read"]} onClose={() => setCreating(false)} />}
    </Card>
  );
}

function KeyRow({ k, onRevoke }: { k: ApiKey; onRevoke: () => void }) {
  const st = stateBadge[k.state];
  return (
    <tr className="border-t border-zinc-100">
      <td className="px-5 py-2.5">
        <p className="font-medium">{k.name}</p>
        <p className="font-mono text-xs text-zinc-500">{k.prefix}_…</p>
      </td>
      <td className="px-3 py-2.5 text-xs">{k.scopes.join(", ")}</td>
      <td className="px-3 py-2.5 text-xs">{k.ip_allowlist.length ? k.ip_allowlist.join(", ") : "Any"}</td>
      <td className="px-3 py-2.5 text-xs">{shortDate(k.expires_at)}</td>
      <td className="px-3 py-2.5 text-xs text-zinc-500">{k.last_used_at ? timeAgo(k.last_used_at) : "Never"}</td>
      <td className="px-5 py-2.5 text-right">
        <span className="inline-flex items-center gap-2">
          <Badge tone={st.tone} dot>
            {st.label}
          </Badge>
          {(k.state === "active" || k.state === "pending_approval") && (
            <Button size="sm" variant="ghost" onClick={onRevoke} aria-label={`Revoke ${k.name}`}>
              Revoke
            </Button>
          )}
        </span>
      </td>
    </tr>
  );
}

function CreateKeyDialog({ slug, scopes, onClose }: { slug: string; scopes: string[]; onClose: () => void }) {
  const create = useCreateApiKey(slug);
  const [name, setName] = useState("");
  const [chosen, setChosen] = useState<string[]>(["contacts:write"]);
  const [ips, setIps] = useState("");
  const [days, setDays] = useState(90);
  const done = create.data;

  return (
    <Dialog open title={done ? "Copy your key now" : "Create API key"} onClose={onClose}>
      {done ? (
        <div className="space-y-3">
          <p className="flex gap-2 rounded-md bg-amber-50 px-3 py-2 text-sm text-amber-900">
            <AlertTriangle className="mt-0.5 size-4 shrink-0" />
            This is the only time the key is shown. Store it in your server's secrets, not in code.
          </p>
          <div className="flex items-center gap-2 rounded-md border border-zinc-200 bg-zinc-50 px-3 py-2">
            <code className="min-w-0 flex-1 truncate text-xs" aria-label="New API key">
              {done.secret}
            </code>
            <CopyButton text={done.secret} label="Copy key" />
          </div>
          {done.key.state === "pending_approval" && (
            <p className="text-sm text-zinc-700">It starts working once another admin approves it in Approvals.</p>
          )}
          <div className="flex justify-end">
            <Button variant="primary" onClick={onClose}>
              Done
            </Button>
          </div>
        </div>
      ) : (
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate({
              name,
              scopes: chosen,
              ip_allowlist: ips
                .split(/[\s,]+/)
                .map((s) => s.trim())
                .filter(Boolean),
              expires_in_days: days,
            });
          }}
        >
          <Field label="Name" hint="Where it's used, e.g. “Website signup form”">
            {(id) => <input id={id} className={inputClass} value={name} onChange={(e) => setName(e.target.value)} />}
          </Field>
          <fieldset>
            <legend className="text-sm font-medium text-zinc-800">Scopes</legend>
            <div className="mt-1.5 space-y-1">
              {scopes.map((s) => (
                <label key={s} className="flex items-center gap-2 text-sm">
                  <input
                    type="checkbox"
                    checked={chosen.includes(s)}
                    onChange={(e) => setChosen(e.target.checked ? [...chosen, s] : chosen.filter((x) => x !== s))}
                  />
                  <code className="text-xs">{s}</code>
                  <span className="text-xs text-zinc-500">{s === "contacts:write" ? "add people, unsubscribe them" : "look up someone's status"}</span>
                </label>
              ))}
            </div>
          </fieldset>
          <Field label="Allowed IP addresses" hint="Your servers' addresses or ranges (203.0.113.7, 198.51.100.0/24). Empty = any address.">
            {(id) => <textarea id={id} rows={2} className={inputClass} value={ips} onChange={(e) => setIps(e.target.value)} />}
          </Field>
          <Field label="Expires after (days)" hint="1 to 90">
            {(id) => <input id={id} type="number" min={1} max={90} className={inputClass} value={days} onChange={(e) => setDays(Number(e.target.value))} />}
          </Field>
          <ErrorNote error={create.error} />
          <div className="flex justify-end gap-2">
            <Button onClick={onClose}>Cancel</Button>
            <Button type="submit" variant="primary" disabled={!name.trim() || chosen.length === 0 || create.isPending}>
              <KeyRound className="size-4" /> {create.isPending ? "Creating…" : "Create key"}
            </Button>
          </div>
        </form>
      )}
    </Dialog>
  );
}

function WordingsCard({ slug, isAdmin }: { slug: string; isAdmin: boolean }) {
  const wordings = useWordings(slug);
  const add = useAddWording(slug);
  const retire = useRetireWording(slug);
  const [label, setLabel] = useState("");
  const [text, setText] = useState("");
  return (
    <Card
      title="Consent wordings"
      description="The exact sentence next to your signup form. It's saved with every sign-up as proof, so it can't be edited: add a new one and retire the old."
    >
      {wordings.isPending ? (
        <Spinner />
      ) : wordings.isError ? (
        <div className="px-5 pb-4">
          <ErrorNote error={wordings.error} />
        </div>
      ) : (
        <ul className="divide-y divide-zinc-100 border-t border-zinc-200">
          {wordings.data.length === 0 && <li className="px-5 py-4 text-sm text-zinc-500">None yet. The API needs one for every sign-up.</li>}
          {wordings.data.map((w) => (
            <li key={w.id} className={`px-5 py-3 ${w.retired_at ? "opacity-60" : ""}`}>
              <div className="flex items-center gap-2">
                <span className="text-sm font-medium">{w.label}</span>
                {w.retired_at ? <Badge>Retired</Badge> : <Badge tone="green">In use</Badge>}
                <span className="ml-auto flex items-center gap-1 font-mono text-[11px] text-zinc-500">
                  {w.id}
                  <CopyButton text={w.id} label="Copy wording id" />
                </span>
              </div>
              <p className="mt-1 text-sm text-zinc-700">“{w.text}”</p>
              {isAdmin && !w.retired_at && (
                <Button size="sm" variant="ghost" className="mt-1" onClick={() => retire.mutate(w.id)} aria-label={`Retire ${w.label}`}>
                  Retire
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
      {isAdmin && (
        <form
          className="space-y-2 border-t border-zinc-200 px-5 py-4"
          onSubmit={(e) => {
            e.preventDefault();
            add.mutate(
              { label, text },
              {
                onSuccess: () => {
                  setLabel("");
                  setText("");
                },
              },
            );
          }}
        >
          <input aria-label="Wording name" placeholder="Name, e.g. Footer signup 2026" className={inputClass} value={label} onChange={(e) => setLabel(e.target.value)} />
          <textarea
            aria-label="Wording text"
            rows={2}
            placeholder="Yes, send me news and offers from this brand. I can unsubscribe any time."
            className={inputClass}
            value={text}
            onChange={(e) => setText(e.target.value)}
          />
          <ErrorNote error={add.error ?? retire.error} />
          <div className="flex justify-end">
            <Button type="submit" size="sm" disabled={!label.trim() || !text.trim() || add.isPending}>
              <Plus className="size-3.5" /> Add wording
            </Button>
          </div>
        </form>
      )}
    </Card>
  );
}

function RepermissionCard({ slug, isAdmin }: { slug: string; isAdmin: boolean }) {
  const rp = useRepermission(slug);
  const set = useSetRepermission(slug);
  if (rp.isPending || rp.isError) return null;
  const d = rp.data;
  return (
    <Card
      title="Re-permission emails"
      description="Ask quarantined contacts from imports to confirm they still want email. Those who confirm become subscribed; the rest are never emailed."
      action={
        isAdmin && (
          <Button size="sm" variant={d.on ? "secondary" : "primary"} disabled={set.isPending || (!d.on && !d.sending_ready)} onClick={() => set.mutate(!d.on)}>
            {d.on ? "Turn off" : "Turn on"}
          </Button>
        )
      }
    >
      <div className="flex flex-wrap gap-6 border-t border-zinc-200 px-5 py-4 text-sm">
        <div>
          <p className="text-xs text-zinc-500">Status</p>
          <Badge tone={d.on ? "green" : "zinc"} dot>
            {d.on ? "Sending" : "Off"}
          </Badge>
        </div>
        <div>
          <p className="text-xs text-zinc-500">Quarantined</p>
          <p className="font-semibold tabular-nums">{d.quarantined.toLocaleString()}</p>
        </div>
        <div>
          <p className="text-xs text-zinc-500">Sent today</p>
          <p className="font-semibold tabular-nums">
            {d.sent_today.toLocaleString()} / {d.per_day.toLocaleString()}
          </p>
        </div>
      </div>
      {!d.sending_ready && (
        <p className="px-5 pb-3 text-xs text-amber-700">Email sending isn't set up yet, so these emails can't go out. They'll be available once it is.</p>
      )}
      <div className="px-5 pb-3">
        <ErrorNote error={set.error} />
      </div>
    </Card>
  );
}

function CallsCard({ slug }: { slug: string }) {
  const calls = useApiCalls(slug, true);
  return (
    <Card title="Recent calls" description="The last 30 days; email addresses are masked">
      {calls.isPending ? (
        <Spinner />
      ) : calls.isError ? (
        <div className="px-5 pb-4">
          <ErrorNote error={calls.error} />
        </div>
      ) : calls.data.length === 0 ? (
        <p className="border-t border-zinc-200 px-5 py-4 text-sm text-zinc-500">No calls yet.</p>
      ) : (
        <table className="w-full border-t border-zinc-200 text-xs">
          <tbody>
            {calls.data.map((c) => (
              <tr key={c.id} className="border-t border-zinc-100 first:border-0">
                <td className="px-5 py-1.5 text-zinc-500">{timeAgo(c.at)}</td>
                <td className="px-3 py-1.5 font-mono">
                  {c.method} {c.path}
                </td>
                <td className="px-3 py-1.5">
                  <Badge tone={c.status < 300 ? "green" : c.status < 500 ? "amber" : "red"}>
                    {c.status}
                    {c.error_code && ` ${c.error_code}`}
                  </Badge>
                </td>
                <td className="px-3 py-1.5">{c.email_mask}</td>
                <td className="px-3 py-1.5 text-zinc-500">{c.key_name}</td>
                <td className="px-5 py-1.5 text-right font-mono text-zinc-500">{c.ip}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Card>
  );
}
