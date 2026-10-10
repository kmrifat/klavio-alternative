import { useEffect, useState } from "react";
import { Link, useParams } from "react-router";
import { Plus, Trash2 } from "lucide-react";
import { useCampaigns, useSaveSender, useSeedActions, useSeeds, useSender } from "../../api/campaigns";
import { Button, Card, ErrorNote, Field, inputClass, PageHeader, Spinner } from "../../components/ui";
import { timeAgo } from "../../lib/format";
import { useBrandAccess } from "../templates/parts";
import { fmt, StateBadge } from "./parts";

// S10: the brand's campaigns, and who its email comes from.
export function CampaignsPage() {
  const slug = useParams().brand!;
  const { brand, canEdit } = useBrandAccess(slug);
  const list = useCampaigns(slug);

  return (
    <div className="max-w-5xl space-y-5">
      <PageHeader
        title="Campaigns"
        description={`Email to one of ${brand?.name ?? "the brand"}'s topics. Every send starts with a small test slice and is watched before the rest goes.`}
        action={
          canEdit && (
            <Link to="new">
              <Button variant="primary">
                <Plus className="size-4" /> New campaign
              </Button>
            </Link>
          )
        }
      />
      <Card>
        {list.isPending ? (
          <div className="px-5">
            <Spinner />
          </div>
        ) : list.isError ? (
          <div className="p-5">
            <ErrorNote error={list.error} />
          </div>
        ) : list.data.length === 0 ? (
          <p className="px-5 py-10 text-center text-sm text-zinc-500">No campaigns yet.{canEdit && " Start one with New campaign."}</p>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-zinc-200 bg-zinc-50 text-left text-[11px] tracking-wide text-zinc-500 uppercase">
                <th className="px-5 py-2 font-medium">Name</th>
                <th className="px-2 py-2 font-medium">Status</th>
                <th className="px-2 py-2 font-medium">Audience</th>
                <th className="px-2 py-2 pr-5 font-medium">Updated</th>
              </tr>
            </thead>
            <tbody>
              {list.data.map((c) => (
                <tr key={c.id} className="border-b border-zinc-100 last:border-0 hover:bg-zinc-50">
                  <td className="px-5 py-3">
                    <Link to={c.id} className="font-medium text-zinc-900 hover:underline">
                      {c.name}
                    </Link>
                    {c.subject && <p className="truncate text-xs text-zinc-500">{c.subject}</p>}
                  </td>
                  <td className="px-2 py-3">
                    <StateBadge state={c.state} />
                  </td>
                  <td className="px-2 py-3 tabular-nums text-zinc-600">{c.audience_count ? fmt(c.audience_count) : "—"}</td>
                  <td className="px-2 py-3 pr-5 text-zinc-600">{timeAgo(c.updated_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>
      <div className="grid gap-5 lg:grid-cols-2">
        <SenderCard slug={slug} />
        <SeedsCard slug={slug} />
      </div>
    </div>
  );
}

function SenderCard({ slug }: { slug: string }) {
  const { brand, canAdmin } = useBrandAccess(slug);
  const sender = useSender(slug);
  const save = useSaveSender(slug);
  const [form, setForm] = useState({ from_name: "", from_local: "", reply_to: "" });
  useEffect(() => {
    if (sender.data) setForm(sender.data.sender);
  }, [sender.data]);

  return (
    <Card title="Sender" description="Every campaign of this brand comes from this address. Campaigns can't pick their own.">
      <div className="border-t border-zinc-100 px-5 py-4">
        {sender.isPending ? (
          <Spinner />
        ) : (
          <form
            className="space-y-3"
            onSubmit={(e) => {
              e.preventDefault();
              save.mutate(form);
            }}
          >
            <p className="font-mono text-xs text-zinc-700">{save.data?.from_header ?? sender.data?.from_header}</p>
            {canAdmin && (
              <>
                <div className="grid gap-3 sm:grid-cols-2">
                  <Field label="Sender name" hint={`Empty: ${brand?.name ?? "the brand name"}`}>
                    {(id) => <input id={id} className={inputClass} value={form.from_name} onChange={(e) => setForm({ ...form, from_name: e.target.value })} />}
                  </Field>
                  <Field label="Address" hint={`@${brand?.sending_domain ?? ""}`}>
                    {(id) => <input id={id} className={inputClass} value={form.from_local} onChange={(e) => setForm({ ...form, from_local: e.target.value })} />}
                  </Field>
                </div>
                <Field label="Replies go to" hint="Optional. Leave empty to keep replies at the sender address.">
                  {(id) => <input id={id} type="email" className={inputClass} value={form.reply_to} onChange={(e) => setForm({ ...form, reply_to: e.target.value })} />}
                </Field>
                <div className="flex items-center justify-end gap-3">
                  <ErrorNote error={save.error} />
                  <Button type="submit" disabled={save.isPending}>
                    {save.isPending ? "Saving…" : "Save sender"}
                  </Button>
                </div>
              </>
            )}
          </form>
        )}
      </div>
    </Card>
  );
}

function SeedsCard({ slug }: { slug: string }) {
  const { canAdmin } = useBrandAccess(slug);
  const seeds = useSeeds(slug);
  const { add, remove } = useSeedActions(slug);
  const [email, setEmail] = useState("");
  return (
    <Card title="Seed list" description="Your team's own inboxes. Test sends go only here.">
      <div className="border-t border-zinc-100 px-5 py-4">
        {seeds.isPending ? (
          <Spinner />
        ) : seeds.data?.length ? (
          <ul aria-label="Seed addresses" className="space-y-1 text-sm">
            {seeds.data.map((s) => (
              <li key={s.email} className="flex items-center justify-between gap-2">
                <span className="font-mono text-xs">{s.email}</span>
                {canAdmin && (
                  <button type="button" aria-label={`Remove ${s.email}`} onClick={() => remove.mutate(s.email)} className="rounded p-1 text-zinc-400 hover:bg-zinc-100 hover:text-zinc-700">
                    <Trash2 className="size-3.5" />
                  </button>
                )}
              </li>
            ))}
          </ul>
        ) : (
          <p className="text-sm text-zinc-500">No seed addresses yet.</p>
        )}
        {canAdmin && (
          <form
            className="mt-3 flex gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              add.mutate(email.trim(), { onSuccess: () => setEmail("") });
            }}
          >
            <input aria-label="Seed address" type="email" placeholder="team@yourcompany.com" className={inputClass} value={email} onChange={(e) => setEmail(e.target.value)} />
            <Button type="submit" disabled={!email.trim() || add.isPending}>
              Add
            </Button>
          </form>
        )}
        <div className="mt-2">
          <ErrorNote error={add.error ?? remove.error} />
        </div>
      </div>
    </Card>
  );
}
