import type { ReactNode } from "react";
import { AlertTriangle, FlaskConical, Info } from "lucide-react";
import { Link } from "react-router";
import { useSendingProvider, type ProviderBrand, type SendingProvider } from "../../api/health";
import { useMe } from "../../api/hooks";
import { Badge, Card, ErrorNote, Spinner } from "../../components/ui";
import { Meter } from "../safety/SafetyCenter";

// How the app sends email: Amazon SES, set up with environment variables on
// the server. Read-only; mirrors GET /admin/v1/sending-provider.
export function SendingProviders() {
  const me = useMe().data!;
  const canSee = me.user.company_role !== "member";
  const provider = useSendingProvider(canSee);

  if (!canSee) {
    return <p className="text-sm text-zinc-600">Only owners and approvers can see how email is sent.</p>;
  }
  if (provider.isPending) return <Spinner />;
  if (provider.isError) return <ErrorNote error={provider.error} />;
  const p = provider.data;

  return (
    <div className="space-y-4">
      {p.mode === "fake" ? (
        <Note tone="amber" icon={<FlaskConical className="mt-0.5 size-4 shrink-0" />}>
          <strong>Test mode: no email leaves this server.</strong> SES_MODE is <code>fake</code>, so sends are only logged and the account
          numbers below are made up. Set <code>SES_MODE=aws</code> to send for real.
        </Note>
      ) : (
        <Note tone="sky" icon={<Info className="mt-0.5 size-4 shrink-0" />}>
          <strong>Read-only.</strong> These come from environment variables on the server. To change one, update it where the API is
          deployed and restart it.
        </Note>
      )}
      <ProviderCard p={p} />
      <AccountCard p={p} />
      <BrandsCard brands={p.brands} />
    </div>
  );
}

function Note({ tone, icon, children }: { tone: "amber" | "sky"; icon: ReactNode; children: ReactNode }) {
  const cls = tone === "amber" ? "border-amber-200 bg-amber-50 text-amber-900" : "border-sky-200 bg-sky-50 text-sky-900";
  return (
    <p className={`flex gap-2 rounded-lg border px-4 py-3 text-sm ${cls}`}>
      {icon}
      <span>{children}</span>
    </p>
  );
}

function ProviderCard({ p }: { p: SendingProvider }) {
  const live = p.mode === "aws";
  const accountRate = p.account.available ? p.account.max_send_rate : 0;
  return (
    <Card
      title="Amazon SES"
      description="Every brand sends through one Amazon Simple Email Service account"
      action={
        <Badge tone={live ? "green" : "amber"} dot>
          {live ? "Sending for real" : "Test mode"}
        </Badge>
      }
    >
      <dl className="divide-y divide-zinc-100 border-t border-zinc-200 text-sm">
        <Row label="Mode" env="SES_MODE" value={live ? "aws: sends real email" : "fake: logs instead of sending"} />
        <Row label="Region" env="SES_REGION" value={p.region} help="The AWS region your SES account and domains are set up in." />
        <Row
          label="Sending speed cap"
          env="SES_MAX_SEND_RATE"
          value={`${p.app_max_send_rate} emails/second`}
          help="The app never sends faster than this, whatever Amazon allows."
          warning={
            accountRate > 0 && p.app_max_send_rate > accountRate
              ? `Faster than the account allows (${accountRate}/second): Amazon will slow sends down. Lower it to ${accountRate}.`
              : undefined
          }
        />
        <Row
          label="Event bus"
          env="SES_EVENT_BUS_ARN"
          value={p.event_bus_arn}
          help="Where Amazon reports what happened to each email (delivered, bounced, marked as spam)."
          warning={live && !p.event_bus_arn ? "Not set: needed when SES_MODE is aws." : undefined}
        />
        <Row
          label="Events queue"
          env="SES_EVENTS_QUEUE_URL"
          value={p.events_queue_url}
          help="Where the app picks those reports up, to stop emailing addresses that bounce or complain."
          warning={live && !p.events_queue_url ? "Not set: bounces and complaints aren't recorded, so bad addresses keep getting email." : undefined}
        />
        <Row label="AWS sign-in" value="The server's AWS credentials" help="An IAM role, AWS_* environment variables or ~/.aws on the server. Never stored in the app." />
      </dl>
    </Card>
  );
}

function Row({ label, env, value, help, warning }: { label: string; env?: string; value: string; help?: string; warning?: string }) {
  return (
    <div className="grid grid-cols-[11rem_1fr] gap-4 px-5 py-3">
      <dt>
        <p className="font-medium">{label}</p>
        {env && <p className="font-mono text-[11px] text-zinc-500">{env}</p>}
      </dt>
      <dd className="min-w-0">
        <p className={value ? "break-all" : "text-zinc-400"}>{value || "Not set"}</p>
        {help && <p className="mt-0.5 text-xs text-zinc-500">{help}</p>}
        {warning && (
          <p className="mt-1 flex gap-1.5 text-xs text-amber-700">
            <AlertTriangle className="mt-px size-3.5 shrink-0" />
            {warning}
          </p>
        )}
      </dd>
    </div>
  );
}

function AccountCard({ p: { account: a, mode } }: { p: SendingProvider }) {
  return (
    <Card
      title="Account limits and standing"
      description={mode === "fake" ? "Made-up numbers while in test mode" : "Read from Amazon, refreshed every minute"}
    >
      <div className="space-y-4 border-t border-zinc-200 px-5 py-4">
        {!a.available ? (
          <p className="text-sm text-red-700">{a.error ?? "Couldn't read the SES account."}</p>
        ) : (
          <>
            <div className="flex flex-wrap gap-2">
              <Badge tone={a.sending_enabled ? "green" : "red"} dot>
                {a.sending_enabled ? "Sending enabled" : "Sending disabled"}
              </Badge>
              <Badge tone={a.production_access ? "green" : "amber"} dot>
                {a.production_access ? "Production access" : "Sandbox"}
              </Badge>
              <Badge tone={a.enforcement_status === "HEALTHY" || !a.enforcement_status ? "green" : "red"} dot>
                Amazon status: {a.enforcement_status || "HEALTHY"}
              </Badge>
            </div>
            {!a.production_access && (
              <p className="text-xs text-amber-700">
                In the SES sandbox only addresses you've verified in AWS receive email. Ask Amazon for production access before
                sending campaigns.
              </p>
            )}
            <Meter
              label="Sent in the last 24 hours"
              used={a.sent_last_24_hours}
              total={a.max_24_hour_send}
              help="Amazon's daily quota for the whole account. To raise it, ask Amazon in the SES console."
            />
            <p className="text-xs text-zinc-600">
              Account rate: <strong>{a.max_send_rate}/second</strong> · more on the <Link to="/safety" className="text-accent hover:underline">Safety center</Link>
            </p>
          </>
        )}
      </div>
    </Card>
  );
}

const identityTone: Record<string, "green" | "amber" | "red"> = { SUCCESS: "green", PENDING: "amber", FAILED: "red" };
const identityLabel: Record<string, string> = { SUCCESS: "Verified", PENDING: "Waiting for DNS", FAILED: "Failed" };

function BrandsCard({ brands }: { brands: ProviderBrand[] }) {
  return (
    <Card title="Brands" description="What SES holds for each brand. Domains are set up from Brands & domains.">
      {brands.length === 0 ? (
        <p className="border-t border-zinc-200 px-5 py-4 text-sm text-zinc-600">
          No brands yet. <Link to="/settings/brands/new" className="text-accent hover:underline">Add one</Link> to set up its sending domain.
        </p>
      ) : (
        <table className="w-full text-sm">
          <thead>
            <tr className="border-y border-zinc-200 bg-zinc-50 text-left text-[11px] tracking-wide text-zinc-500 uppercase">
              <th className="px-5 py-2 font-medium">Brand</th>
              <th className="px-3 py-2 font-medium">Sends from</th>
              <th className="px-5 py-2 text-right font-medium">Domain in SES</th>
            </tr>
          </thead>
          <tbody>
            {brands.map((b) => (
              <tr key={b.slug} className="border-b border-zinc-100 last:border-0">
                <td className="px-5 py-2.5">
                  <Link to={`/settings/brands/${b.slug}`} className="flex items-center gap-2 font-medium hover:underline">
                    <span className="size-2.5 rounded-full" style={{ background: b.color }} />
                    {b.name}
                  </Link>
                </td>
                <td className="px-3 py-2.5 [overflow-wrap:anywhere]">
                  <p>{b.sending_domain}</p>
                  <p className="text-xs text-zinc-500">Bounces via {b.mail_from_domain}</p>
                  <p className="text-xs text-zinc-500">
                    Configuration set <span className="font-mono">{b.ses_configuration_set}</span>
                  </p>
                </td>
                <td className="px-5 py-2.5 text-right">
                  <Badge tone={identityTone[b.ses_identity_status] ?? "amber"} dot>
                    {identityLabel[b.ses_identity_status] ?? b.ses_identity_status.toLowerCase()}
                  </Badge>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Card>
  );
}
