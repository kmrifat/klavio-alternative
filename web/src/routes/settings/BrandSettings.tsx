import { useState } from "react";
import { Link, useParams } from "react-router";
import { AlertTriangle, ArrowLeft, CheckCircle2, Clock, RefreshCw, XCircle } from "lucide-react";
import { useBrand, useCheckDNS, useMe, useUpdateBrand } from "../../api/hooks";
import type { Brand, DNSRecord } from "../../api/types";
import { Badge, Button, Card, CopyButton, ErrorNote, Field, inputClass, Spinner } from "../../components/ui";
import { BrandMark } from "../../layout/BrandSwitcher";
import { brandStatus, dnsPurpose, timeAgo } from "../../lib/format";
import { NotFound } from "../NotFound";
import { ApiError } from "../../api/client";

export function BrandSettings() {
  const slug = useParams().slug!;
  const detail = useBrand(slug);
  const check = useCheckDNS(slug);

  if (detail.isPending) return <Spinner />;
  if (detail.isError) {
    return detail.error instanceof ApiError && detail.error.status === 404 ? <NotFound /> : <ErrorNote error={detail.error} />;
  }
  const { brand, dns_records } = detail.data;
  const st = brandStatus[brand.status];

  return (
    <div className="space-y-5">
      <Link to="/settings/brands" className="inline-flex items-center gap-1 text-sm text-zinc-600 hover:text-zinc-900">
        <ArrowLeft className="size-4" /> Brands
      </Link>

      <div className="flex items-center gap-3">
        <BrandMark brand={brand} />
        <div className="flex-1">
          <h2 className="text-lg font-semibold">{brand.name}</h2>
          <p className="font-mono text-xs text-zinc-500">{brand.sending_domain}</p>
        </div>
        <Badge tone={st.tone} dot>
          {st.label}
        </Badge>
      </div>

      {brand.status === "setup" && (
        <div role="note" className="flex gap-3 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-900">
          <AlertTriangle className="mt-0.5 size-4 shrink-0" />
          <div>
            <p className="font-medium">Sending is blocked until DNS is right</p>
            <p className="text-amber-800">
              Add these records at your DNS provider for <strong>{brand.sending_domain}</strong>, then press Re-check. Changes can take a
              few minutes to appear. SES identity: {brand.ses_identity_status.toLowerCase()}.
            </p>
          </div>
        </div>
      )}

      <Card
        title="DNS records"
        description={`Checked ${timeAgo(brand.dns_checked_at)} · re-checked daily · a failing record pauses sending for this brand`}
        action={
          <Button size="sm" onClick={() => check.mutate()} disabled={check.isPending}>
            <RefreshCw className={`size-3.5 ${check.isPending ? "animate-spin" : ""}`} />
            {check.isPending ? "Checking…" : "Re-check now"}
          </Button>
        }
      >
        {check.isError && (
          <div className="px-5 pb-3">
            <ErrorNote error={check.error} />
          </div>
        )}
        <DNSTable records={dns_records} />
      </Card>

      <DetailsCard brand={brand} />
    </div>
  );
}

const statusIcon = {
  ok: <CheckCircle2 className="size-4 text-green-600" aria-label="OK" />,
  warning: <AlertTriangle className="size-4 text-amber-600" aria-label="Warning" />,
  failing: <XCircle className="size-4 text-red-600" aria-label="Failing" />,
  pending: <Clock className="size-4 text-zinc-400" aria-label="Not checked yet" />,
};

function DNSTable({ records }: { records: DNSRecord[] }) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="border-y border-zinc-200 bg-zinc-50 text-left text-[11px] tracking-wide text-zinc-500 uppercase">
            <th className="w-8 px-5 py-2" />
            <th className="px-2 py-2 font-medium">Purpose</th>
            <th className="px-2 py-2 font-medium">Type</th>
            <th className="px-2 py-2 font-medium">Name</th>
            <th className="px-2 py-2 pr-5 font-medium">Value</th>
          </tr>
        </thead>
        <tbody>
          {records.map((r) => (
            <tr key={r.id} className="border-b border-zinc-100 align-top last:border-0">
              <td className="px-5 py-3">{statusIcon[r.status]}</td>
              <td className="px-2 py-3">
                <p className="font-medium whitespace-nowrap">{dnsPurpose[r.purpose]}</p>
                {r.detail && <p className={`mt-0.5 text-xs ${r.status === "failing" ? "text-red-600" : "text-zinc-500"}`}>{r.detail}</p>}
              </td>
              <td className="px-2 py-3 font-mono text-xs">{r.type}</td>
              <td className="px-2 py-3">
                <span className="flex items-center gap-1 font-mono text-xs break-all">
                  {r.name}
                  <CopyButton text={r.name} label="Copy name" />
                </span>
              </td>
              <td className="px-2 py-3 pr-5">
                <span className="flex items-center gap-1 font-mono text-xs break-all">
                  {r.value}
                  <CopyButton text={r.value} label="Copy value" />
                </span>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function DetailsCard({ brand }: { brand: Brand }) {
  const me = useMe().data!;
  const canEdit = me.user.company_role === "owner" || me.brand_roles[brand.id] === "brand_admin";
  const update = useUpdateBrand(brand.slug);
  const [form, setForm] = useState({ name: brand.name, color: brand.color, postal_address: brand.postal_address });
  const dirty = form.name !== brand.name || form.color !== brand.color || form.postal_address !== brand.postal_address;

  return (
    <Card title="Details" description="Domains can't change — a new domain is a new sender with its own reputation, so add a new brand instead.">
      <form
        className="grid gap-4 border-t border-zinc-100 px-5 py-5 sm:grid-cols-2"
        onSubmit={(e) => {
          e.preventDefault();
          update.mutate(form);
        }}
      >
        <Field label="Brand name">
          {(id) => <input id={id} disabled={!canEdit} className={inputClass} value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />}
        </Field>
        <Field label="Brand color">
          {(id) => (
            <input id={id} type="color" disabled={!canEdit} className="h-9 w-12 rounded border border-zinc-200" value={form.color} onChange={(e) => setForm({ ...form, color: e.target.value })} />
          )}
        </Field>
        <Field label="Postal address">
          {(id) => (
            <textarea id={id} rows={2} disabled={!canEdit} className={inputClass} value={form.postal_address} onChange={(e) => setForm({ ...form, postal_address: e.target.value })} />
          )}
        </Field>
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 self-start text-sm">
          <dt className="text-zinc-500">Sending</dt>
          <dd className="font-mono text-xs leading-5">{brand.sending_domain}</dd>
          <dt className="text-zinc-500">Return-Path</dt>
          <dd className="font-mono text-xs leading-5">{brand.mail_from_domain}</dd>
          <dt className="text-zinc-500">Links</dt>
          <dd className="font-mono text-xs leading-5">{brand.link_domain}</dd>
          <dt className="text-zinc-500">SES config set</dt>
          <dd className="font-mono text-xs leading-5">{brand.ses_configuration_set}</dd>
        </dl>
        {canEdit && (
          <div className="flex items-center justify-end gap-3 sm:col-span-2">
            <ErrorNote error={update.error} />
            {update.isSuccess && !dirty && <span className="text-sm text-green-700">Saved</span>}
            <Button type="submit" variant="primary" disabled={!dirty || update.isPending}>
              {update.isPending ? "Saving…" : "Save"}
            </Button>
          </div>
        )}
      </form>
    </Card>
  );
}
