import { useState } from "react";
import { useInvite, useSetRoles } from "../../api/hooks";
import type { BrandListItem, BrandRole, CompanyRole, Member } from "../../api/types";
import { Button, CopyButton, Dialog, ErrorNote, Field, inputClass } from "../../components/ui";
import { brandRoleHelp, brandRoleLabel } from "../../lib/format";

const brandRoles: BrandRole[] = ["viewer", "editor", "sender", "brand_admin"];

function CompanyRoleSelect({ id, value, onChange, disabled }: {
  id: string;
  value: CompanyRole;
  onChange: (r: CompanyRole) => void;
  disabled?: boolean;
}) {
  return (
    <select id={id} className={inputClass} value={value} disabled={disabled} onChange={(e) => onChange(e.target.value as CompanyRole)}>
      <option value="member">Member — brand roles only</option>
      <option value="approver">Approver — can view and approve for every brand</option>
      <option value="owner">Owner — everything</option>
    </select>
  );
}

// BrandRolePicker: one select per brand; "" = no access.
function BrandRolePicker({ brands, value, onChange }: {
  brands: BrandListItem[];
  value: Record<string, BrandRole | "">;
  onChange: (v: Record<string, BrandRole | "">) => void;
}) {
  if (brands.length === 0) return <p className="text-sm text-zinc-500">No brands yet.</p>;
  return (
    <div className="space-y-2">
      {brands.map((b) => (
        <div key={b.id} className="flex items-center gap-3">
          <span className="w-32 truncate text-sm">{b.name}</span>
          <select
            aria-label={`Role for ${b.name}`}
            className={inputClass}
            value={value[b.id] ?? ""}
            onChange={(e) => onChange({ ...value, [b.id]: e.target.value as BrandRole | "" })}
          >
            <option value="">No access</option>
            {brandRoles.map((r) => (
              <option key={r} value={r}>
                {brandRoleLabel[r]} — {brandRoleHelp[r]}
              </option>
            ))}
          </select>
        </div>
      ))}
    </div>
  );
}

export function InviteDialog({ open, onClose, brands }: { open: boolean; onClose: () => void; brands: BrandListItem[] }) {
  const invite = useInvite();
  const [email, setEmail] = useState("");
  const [name, setName] = useState("");
  const [companyRole, setCompanyRole] = useState<CompanyRole>("member");
  const [roles, setRoles] = useState<Record<string, BrandRole | "">>({});

  const close = () => {
    invite.reset();
    setEmail("");
    setName("");
    setCompanyRole("member");
    setRoles({});
    onClose();
  };

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const brand_roles = Object.fromEntries(Object.entries(roles).filter(([, r]) => r)) as Record<string, BrandRole>;
    invite.mutate({ email, name, company_role: companyRole, brand_roles });
  };

  return (
    <Dialog open={open} title="Invite member" onClose={close}>
      {invite.data ? (
        <div className="space-y-3">
          <p className="text-sm">
            Send this link to <strong>{email}</strong>. It works once, expires in 7 days and{" "}
            <strong>won't be shown again</strong>.
          </p>
          <div className="flex items-center gap-2 rounded-md border border-zinc-200 bg-zinc-50 px-3 py-2">
            <code className="min-w-0 flex-1 truncate text-xs">{invite.data.invite_url}</code>
            <CopyButton text={invite.data.invite_url} label="Copy invite link" />
          </div>
          <div className="flex justify-end">
            <Button variant="primary" onClick={close}>
              Done
            </Button>
          </div>
        </div>
      ) : (
        <form onSubmit={submit} className="space-y-4">
          <Field label="Work email">
            {(id) => <input id={id} type="email" required className={inputClass} value={email} onChange={(e) => setEmail(e.target.value)} />}
          </Field>
          <Field label="Name">
            {(id) => <input id={id} required className={inputClass} value={name} onChange={(e) => setName(e.target.value)} />}
          </Field>
          <Field label="Company role">{(id) => <CompanyRoleSelect id={id} value={companyRole} onChange={setCompanyRole} />}</Field>
          {companyRole !== "owner" && (
            <div>
              <p className="mb-1.5 text-sm font-medium">Brand access</p>
              <BrandRolePicker brands={brands} value={roles} onChange={setRoles} />
            </div>
          )}
          <ErrorNote error={invite.error} />
          <div className="flex justify-end gap-2">
            <Button onClick={close}>Cancel</Button>
            <Button type="submit" variant="primary" disabled={invite.isPending}>
              {invite.isPending ? "Creating…" : "Create invite link"}
            </Button>
          </div>
        </form>
      )}
    </Dialog>
  );
}

export function EditRolesDialog({ member, brands, isSelf, onClose }: {
  member: Member;
  brands: BrandListItem[];
  isSelf: boolean;
  onClose: () => void;
}) {
  const setRolesM = useSetRoles();
  const [companyRole, setCompanyRole] = useState<CompanyRole>(member.company_role);
  // Every brand is sent, so "No access" removes a role.
  const [roles, setRoles] = useState<Record<string, BrandRole | "">>(() =>
    Object.fromEntries(brands.map((b) => [b.id, member.brand_roles[b.id] ?? ""])),
  );

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    setRolesM.mutate({ id: member.id, company_role: companyRole, brand_roles: roles }, { onSuccess: onClose });
  };

  return (
    <Dialog open title={`Roles for ${member.name}`} onClose={onClose}>
      <form onSubmit={submit} className="space-y-4">
        <Field label="Company role" hint={isSelf ? "You can't change your own company role. Ask another owner." : undefined}>
          {(id) => <CompanyRoleSelect id={id} value={companyRole} onChange={setCompanyRole} disabled={isSelf} />}
        </Field>
        {companyRole === "owner" ? (
          <p className="text-sm text-zinc-500">Owners are brand admins on every brand.</p>
        ) : (
          <div>
            <p className="mb-1.5 text-sm font-medium">Brand access</p>
            <BrandRolePicker brands={brands} value={roles} onChange={setRoles} />
          </div>
        )}
        <ErrorNote error={setRolesM.error} />
        <div className="flex justify-end gap-2">
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={setRolesM.isPending}>
            {setRolesM.isPending ? "Saving…" : "Save roles"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
