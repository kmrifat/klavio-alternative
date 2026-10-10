import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRound } from "lucide-react";
import { useNavigate, useParams } from "react-router";
import { apiFetch } from "../../api/client";
import { acceptInviteWithPasskey } from "../../api/passkeys";
import { Button, ErrorNote, Spinner } from "../../components/ui";
import { AuthShell } from "./AuthShell";

type InviteInfo = { email: string; name: string; expires_at: string };

export function AcceptInvite() {
  const token = useParams().token!;
  const qc = useQueryClient();
  const navigate = useNavigate();

  const invite = useQuery({
    queryKey: ["invite", token],
    queryFn: () => apiFetch<InviteInfo>(`/admin/v1/invites/${token}`),
    retry: false,
  });

  const accept = useMutation({
    mutationFn: () => acceptInviteWithPasskey(token),
    onSuccess: (me) => {
      qc.setQueryData(["me"], me);
      navigate("/", { replace: true });
    },
  });

  if (invite.isPending) return <Spinner />;

  return (
    <AuthShell>
      {invite.isError ? (
        <>
          <h1 className="text-lg font-semibold">Invite not valid</h1>
          <div className="mt-3">
            <ErrorNote error={invite.error} />
          </div>
          <p className="mt-3 text-sm text-zinc-600">Ask the person who invited you for a new link.</p>
        </>
      ) : (
        <>
          <h1 className="text-lg font-semibold">Welcome, {invite.data.name}</h1>
          <p className="mt-1 text-sm text-zinc-600">
            Create a passkey for <strong className="font-medium text-zinc-900">{invite.data.email}</strong>. You'll use it
            to sign in and to confirm sensitive actions.
          </p>
          <Button variant="primary" className="mt-5 w-full" disabled={accept.isPending} onClick={() => accept.mutate()}>
            <KeyRound className="size-4" />
            {accept.isPending ? "Waiting for your passkey…" : "Create passkey"}
          </Button>
          <div className="mt-4">
            <ErrorNote error={accept.error} />
          </div>
          <p className="mt-5 border-t border-zinc-100 pt-4 text-xs text-zinc-500">
            This link works once and expires {new Date(invite.data.expires_at).toLocaleDateString()}.
          </p>
        </>
      )}
    </AuthShell>
  );
}
