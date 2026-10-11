import { useState, type FormEvent, type ReactNode } from "react";
import { Check } from "lucide-react";
import { useCompany, useRenameCompany } from "../../api/company";
import { useMe } from "../../api/hooks";
import { Badge, Button, Card, ErrorNote, Field, Spinner, inputClass } from "../../components/ui";

// Settings → General: the company's name, and how this server is set up.
export function General() {
  const company = useCompany();
  if (company.isPending) return <Spinner />;
  if (company.isError) return <ErrorNote error={company.error} />;
  const c = company.data;
  const i = c.installation;
  return (
    <div className="space-y-4">
      <NameCard name={c.name} shown={c.display_name} />
      <Card title="This installation" description="Set on the server with environment variables; shown here for reference">
        <dl className="divide-y divide-zinc-100 border-t border-zinc-200 text-sm">
          <Row label="Version" value={i.version} />
          <Row
            label="Environment"
            value={
              <Badge tone={i.env === "production" ? "green" : "amber"} dot>
                {i.env === "production" ? "Production" : "Development"}
              </Badge>
            }
            env="APP_ENV"
          />
          <Row label="Web app address" value={i.app_url} env="APP_URL" help="Used in invite links." />
          <Row
            label="Passkey domain"
            value={i.passkey_domain}
            env="WEBAUTHN_RP_ID"
            help="Passkeys only work on this domain and its subdomains. Changing it means everyone sets up their passkey again."
          />
          <Row
            label="Cloudflare Access"
            value={i.cloudflare_access ? "On: a Cloudflare login is needed before the passkey" : "Off"}
            env="CF_ACCESS_TEAM_DOMAIN"
            help={i.cloudflare_access ? undefined : "Required in production, as a second lock in front of the app."}
          />
          <Row
            label="Debug aids"
            value={i.debug ? "On: the admin API reference is served" : "Off"}
            env="DEBUG"
            help={i.debug && i.env === "production" ? "Turn this off when you're done." : undefined}
          />
        </dl>
      </Card>
    </div>
  );
}

function NameCard({ name, shown }: { name: string; shown: string }) {
  const isOwner = useMe().data?.user.company_role === "owner";
  const rename = useRenameCompany();
  const [draft, setDraft] = useState<string | null>(null);
  const value = draft ?? name;
  const changed = value.trim().replace(/\s+/g, " ") !== name;

  const save = (e: FormEvent) => {
    e.preventDefault();
    rename.mutate(value, { onSuccess: () => setDraft(null) });
  };

  return (
    <Card title="Company" description="Shown in the sidebar and on the sign-in page">
      <form onSubmit={save} className="space-y-3 border-t border-zinc-200 px-5 py-4">
        <Field
          label="Company name"
          hint={isOwner ? (name ? undefined : `Not set yet, so the app shows "${shown}".`) : "Only owners can change it."}
        >
          {(id) => (
            <input
              id={id}
              className={inputClass}
              value={value}
              maxLength={80}
              disabled={!isOwner}
              placeholder={shown}
              onChange={(e) => {
                rename.reset();
                setDraft(e.target.value);
              }}
            />
          )}
        </Field>
        <ErrorNote error={rename.error} />
        {isOwner && (
          <div className="flex items-center justify-end gap-3">
            {rename.isSuccess && !changed && (
              <span role="status" className="flex items-center gap-1 text-xs text-green-700">
                <Check className="size-3.5" /> Saved
              </span>
            )}
            <Button type="submit" variant="primary" disabled={!changed || !value.trim() || rename.isPending}>
              {rename.isPending ? "Saving…" : "Save"}
            </Button>
          </div>
        )}
      </form>
    </Card>
  );
}

function Row({ label, value, env, help }: { label: string; value: ReactNode; env?: string; help?: string }) {
  return (
    <div className="grid grid-cols-[11rem_1fr] gap-4 px-5 py-3">
      <dt>
        <p className="font-medium">{label}</p>
        {env && <p className="font-mono text-[11px] text-zinc-500">{env}</p>}
      </dt>
      <dd className="min-w-0">
        <div className="[overflow-wrap:anywhere]">{value}</div>
        {help && <p className="mt-0.5 text-xs text-zinc-500">{help}</p>}
      </dd>
    </div>
  );
}
