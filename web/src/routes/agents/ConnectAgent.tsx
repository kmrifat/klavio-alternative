import { Bot, Check, TriangleAlert, X } from "lucide-react";
import { useParams } from "react-router";
import { useDecideOAuthGrant, useOAuthGrant } from "../../api/oauth";
import { useCompanyName, useMe } from "../../api/hooks";
import { Button, ErrorNote, Spinner } from "../../components/ui";
import { companyRoleLabel } from "../../lib/format";
import { AuthShell } from "../auth/AuthShell";

// /connect/:id — an AI client (claude.ai, Claude Desktop) asks to act as the
// signed-in person. Approving needs their passkey.
export function ConnectAgent() {
  const id = useParams().id!;
  const grant = useOAuthGrant(id);
  const decide = useDecideOAuthGrant(id);
  const me = useMe().data!;
  const company = useCompanyName();

  if (grant.isPending)
    return (
      <AuthShell>
        <Spinner />
      </AuthShell>
    );
  if (grant.isError)
    return (
      <AuthShell>
        <ErrorNote error={grant.error} />
      </AuthShell>
    );
  const g = grant.data;

  if (g.status !== "pending") {
    const why = {
      approved: "This connection was already approved.",
      denied: "This connection was turned down.",
      expired: "This request expired: requests last 10 minutes.",
    }[g.status];
    return (
      <AuthShell>
        <h1 className="text-base font-semibold">Nothing to approve</h1>
        <p className="mt-2 text-sm text-zinc-600">
          {why} To connect, start again from your AI app.
        </p>
      </AuthShell>
    );
  }
  if (decide.isSuccess) {
    return (
      <AuthShell>
        <p className="text-sm text-zinc-700">
          Sending you back to {g.redirect_host}…
        </p>
      </AuthShell>
    );
  }

  return (
    <AuthShell>
      <div className="flex items-center gap-2">
        <span className="grid size-8 place-items-center rounded-lg bg-violet-100 text-violet-700">
          <Bot className="size-4" />
        </span>
        <h1 className="text-base font-semibold">
          Connect {g.client_name} to {company}?
        </h1>
      </div>
      <p className="mt-3 text-sm text-zinc-700">
        It will act as you, <strong>{me.user.name}</strong> (
        {companyRoleLabel[me.user.company_role].toLowerCase()}), for{" "}
        {g.token_days} days, with at most your access.
      </p>
      <ul className="mt-3 space-y-1 text-sm">
        <li className="flex gap-2">
          <Check className="mt-0.5 size-4 shrink-0 text-green-600" /> Read
          reports, health, campaigns, templates, segments and contacts
        </li>
        <li className="flex gap-2">
          <Check className="mt-0.5 size-4 shrink-0 text-green-600" /> Prepare
          drafts, and ask you to approve a send
        </li>
        <li className="flex gap-2">
          <X className="mt-0.5 size-4 shrink-0 text-red-600" /> Never send,
          approve, delete, block people or change settings
        </li>
      </ul>
      <p className="mt-3 flex gap-2 rounded-md bg-amber-50 px-3 py-2 text-xs text-amber-900">
        <TriangleAlert className="mt-px size-3.5 shrink-0" />
        <span>
          Only continue if you just asked {g.client_name} to connect. You'll go
          back to <strong>{g.redirect_host}</strong>. The name is what the app
          calls itself.
        </span>
      </p>
      <ErrorNote error={decide.error} />
      <div className="mt-4 flex justify-end gap-2">
        <Button
          onClick={() => decide.mutate(false)}
          disabled={decide.isPending}
        >
          Cancel
        </Button>
        <Button
          variant="primary"
          onClick={() => decide.mutate(true)}
          disabled={decide.isPending}
        >
          {decide.isPending ? "Confirming…" : "Allow with passkey"}
        </Button>
      </div>
      <p className="mt-3 text-xs text-zinc-500">
        You can disconnect it any time in Settings → AI agents.
      </p>
    </AuthShell>
  );
}
