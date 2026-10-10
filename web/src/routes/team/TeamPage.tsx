import { useState } from "react";
import { BadgeCheck, Fingerprint, History, ShieldCheck, UserPlus, Users } from "lucide-react";
import { useAudit, useBrands, useMe, useTeam } from "../../api/hooks";
import type { BrandListItem, BrandRole, Member } from "../../api/types";
import { Badge, Button, Card, ErrorNote, PageHeader, Spinner } from "../../components/ui";
import { auditLabel, brandRoleLabel, companyRoleLabel, initials, timeAgo } from "../../lib/format";
import { EditRolesDialog, InviteDialog } from "./TeamDialogs";
import { useUnfreeze } from "../../api/governance";

export function TeamPage() {
  const me = useMe().data!;
  const team = useTeam();
  const brands = useBrands();
  const isOwner = me.user.company_role === "owner";
  const seesAudit = me.user.company_role !== "member";
  const [inviting, setInviting] = useState(false);
  const [editing, setEditing] = useState<Member | null>(null);

  return (
    <div className="max-w-6xl space-y-5">
      <PageHeader
        title="Team & access"
        description="Who can do what, per brand. Sensitive actions need a passkey confirmation and are always logged."
        action={
          isOwner && (
            <Button variant="primary" onClick={() => setInviting(true)}>
              <UserPlus className="size-4" /> Invite member
            </Button>
          )
        }
      />

      <SecurityCard />

      <Card title="Members & roles per brand" description="Viewer: reports without personal data · Editor: build campaigns · Sender: schedule within limits · Brand admin: approve, import, manage the domain · Owner: everything">
        {team.isPending || brands.isPending ? (
          <div className="px-5">
            <Spinner />
          </div>
        ) : team.isError ? (
          <div className="px-5 pb-5">
            <ErrorNote error={team.error} />
          </div>
        ) : (
          <MembersTable
            members={team.data.members}
            brands={brands.data ?? []}
            meId={me.user.id}
            onEdit={isOwner ? setEditing : undefined}
          />
        )}
        {team.data && team.data.invites.length > 0 && (
          <div className="border-t border-zinc-200 px-5 py-3">
            <h3 className="text-xs font-medium tracking-wide text-zinc-500 uppercase">Pending invites</h3>
            <ul className="mt-2 space-y-1.5">
              {team.data.invites.map((i) => (
                <li key={i.id} className="flex items-center justify-between text-sm">
                  <span>
                    {i.name} <span className="text-zinc-500">· {i.email}</span>
                  </span>
                  <span className="flex items-center gap-2 text-xs text-zinc-500">
                    {i.awaiting_approval && <Badge tone="amber">Waiting for approval</Badge>}
                    expires {new Date(i.expires_at).toLocaleDateString()}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        )}
      </Card>

      <div className="grid gap-5 lg:grid-cols-[1fr_minmax(0,22rem)]">
        <SecondPersonCard />
        {seesAudit && <AuditCard />}
      </div>

      <InviteDialog open={inviting} onClose={() => setInviting(false)} brands={brands.data ?? []} />
      {editing && (
        <EditRolesDialog member={editing} brands={brands.data ?? []} isSelf={editing.id === me.user.id} onClose={() => setEditing(null)} />
      )}
    </div>
  );
}

function SecurityCard() {
  const items = [
    { icon: Fingerprint, title: "Passkeys required to sign in", body: "Phishing-resistant. One-time codes and SMS are not accepted.", badge: <Badge tone="green" dot>Enforced</Badge> },
    { icon: ShieldCheck, title: "Re-confirm for sensitive actions", body: "Passkey prompt before brands, invites and role changes.", badge: <Badge tone="green" dot>Enforced</Badge> },
    { icon: BadgeCheck, title: "Behind Cloudflare Access", body: "Company Google accounts on managed devices only. Must match the app account.", badge: <Badge tone="blue">In production</Badge> },
    { icon: History, title: "Anomaly alerts", body: "New device or country, volume spikes, mass exports → freeze + alert.", badge: <Badge>Coming in M5</Badge> },
  ];
  return (
    <Card title="Security" description="Account takeover is the biggest real-world risk for email tools — these are enforced for everyone.">
      <ul className="grid gap-x-8 px-5 pb-4 sm:grid-cols-2">
        {items.map(({ icon: Icon, title, body, badge }) => (
          <li key={title} className="flex items-start gap-3 border-t border-zinc-100 py-3">
            <Icon className="mt-0.5 size-4 shrink-0 text-zinc-500" />
            <div className="min-w-0 flex-1">
              <p className="text-sm font-medium">{title}</p>
              <p className="text-xs text-zinc-500">{body}</p>
            </div>
            {badge}
          </li>
        ))}
      </ul>
    </Card>
  );
}

function RoleBadge({ role }: { role: BrandRole | undefined }) {
  if (!role) return <span className="text-xs text-zinc-400">No access</span>;
  const tone = role === "brand_admin" ? "indigo" : role === "sender" ? "blue" : "zinc";
  return <Badge tone={tone}>{brandRoleLabel[role]}</Badge>;
}

function MembersTable({ members, brands, meId, onEdit }: {
  members: Member[];
  brands: BrandListItem[];
  meId: string;
  onEdit?: (m: Member) => void;
}) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="border-y border-zinc-200 bg-zinc-50 text-left text-[11px] tracking-wide text-zinc-500 uppercase">
            <th className="px-5 py-2 font-medium">Member</th>
            {brands.map((b) => (
              <th key={b.id} className="px-3 py-2 font-medium">
                {b.name}
              </th>
            ))}
            <th className="px-3 py-2 font-medium">Company</th>
            <th className="px-5 py-2 text-right font-medium">Last active</th>
          </tr>
        </thead>
        <tbody>
          {members.map((m) => (
            <tr key={m.id} className="border-b border-zinc-100 last:border-0">
              <td className="px-5 py-3">
                <div className="flex items-center gap-2.5">
                  <span className="grid size-7 shrink-0 place-items-center rounded-full bg-zinc-100 text-[11px] font-semibold text-zinc-600">
                    {initials(m.name)}
                  </span>
                  <div className="min-w-0">
                    <p className="font-medium">
                      {m.name}
                      {m.id === meId && <span className="font-normal text-zinc-500"> (you)</span>}
                    </p>
                    <p className="text-xs text-zinc-500">
                      {m.email} · {m.passkeys} {m.passkeys === 1 ? "passkey" : "passkeys"}
                    </p>
                    {m.frozen_at && (
                      <p className="mt-1 flex flex-wrap items-center gap-2 text-xs text-red-700">
                        <Badge tone="red" dot>
                          Frozen
                        </Badge>
                        {m.frozen_reason}
                        {onEdit && <UnfreezeButton id={m.id} name={m.name} />}
                      </p>
                    )}
                  </div>
                </div>
              </td>
              {brands.map((b) => (
                <td key={b.id} className="px-3 py-3">
                  <RoleBadge role={m.company_role === "owner" ? "brand_admin" : m.brand_roles[b.id]} />
                </td>
              ))}
              <td className="px-3 py-3">
                {m.company_role === "member" ? (
                  <span className="text-zinc-400">{companyRoleLabel.member}</span>
                ) : (
                  <Badge tone={m.company_role === "owner" ? "indigo" : "blue"}>{companyRoleLabel[m.company_role]}</Badge>
                )}
              </td>
              <td className="px-5 py-3 text-right text-zinc-500">
                <div className="flex items-center justify-end gap-3">
                  {timeAgo(m.last_active_at)}
                  {onEdit && (
                    <Button size="sm" variant="ghost" onClick={() => onEdit(m)} aria-label={`Edit roles for ${m.name}`}>
                      Edit
                    </Button>
                  )}
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function SecondPersonCard() {
  const items = [
    ["Large sends", "Over 10,000 people or 2× the brand's usual size"],
    ["Riskier audiences", "Any At-risk contacts · first send to an imported list"],
    ["Brands in warm-up", "Every send until warm-up is complete"],
    ["Exports with personal data", "Always · download link expires in 24 h · watermarked"],
    ["Admin changes", "API keys, DNS, roles, safety limits, resuming a paused brand"],
  ];
  return (
    <Card title="Needs a second person" description="The requester can never approve their own request. Arrives with Approvals in M5.">
      <ul className="px-5 pb-3">
        {items.map(([title, body]) => (
          <li key={title} className="flex items-center justify-between gap-3 border-t border-zinc-100 py-2.5">
            <div>
              <p className="text-sm font-medium">{title}</p>
              <p className="text-xs text-zinc-500">{body}</p>
            </div>
            <Badge tone="zinc">Approval · M5</Badge>
          </li>
        ))}
      </ul>
    </Card>
  );
}

function AuditCard() {
  const audit = useAudit(true);
  return (
    <Card title="Audit log" description="Append-only. Who, what, which brand, before → after, device and IP.">
      <div className="px-5 pb-4">
        {audit.isPending ? (
          <Spinner />
        ) : audit.isError ? (
          <ErrorNote error={audit.error} />
        ) : audit.data.length === 0 ? (
          <p className="py-4 text-sm text-zinc-500">Nothing yet.</p>
        ) : (
          <ul className="space-y-3">
            {audit.data.slice(0, 6).map((e) => (
              <li key={e.id} className="flex items-start gap-2.5">
                <Users className="mt-0.5 size-4 shrink-0 text-zinc-400" />
                <div className="min-w-0 flex-1">
                  <p className="text-sm">
                    {auditLabel[e.action] ?? e.action} <span className="font-medium">{e.target_id}</span>
                  </p>
                  <p className="truncate text-xs text-zinc-500">{e.actor_email || "system"}</p>
                </div>
                <span className="shrink-0 text-xs text-zinc-500">{timeAgo(e.at)}</span>
              </li>
            ))}
          </ul>
        )}
      </div>
    </Card>
  );
}

function UnfreezeButton({ id, name }: { id: string; name: string }) {
  const unfreeze = useUnfreeze();
  return (
    <>
      <Button size="sm" variant="ghost" disabled={unfreeze.isPending} onClick={() => unfreeze.mutate(id)} aria-label={`Unfreeze ${name}`}>
        Unfreeze
      </Button>
      <ErrorNote error={unfreeze.error} />
    </>
  );
}
