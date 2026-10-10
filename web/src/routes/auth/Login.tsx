import { useMutation, useQueryClient } from "@tanstack/react-query";
import { KeyRound } from "lucide-react";
import { Navigate, useNavigate, useSearchParams } from "react-router";
import { ApiError } from "../../api/client";
import { useMe } from "../../api/hooks";
import { signInWithPasskey } from "../../api/passkeys";
import { Button, ErrorNote, Spinner } from "../../components/ui";
import { AuthShell } from "./AuthShell";

// safeNext only allows paths inside this app, so ?next= can't redirect elsewhere.
function safeNext(next: string | null): string {
  return next && next.startsWith("/") && !next.startsWith("//") ? next : "/";
}

export function Login() {
  const me = useMe();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const next = safeNext(useSearchParams()[0].get("next"));

  const signIn = useMutation({
    mutationFn: signInWithPasskey,
    onSuccess: (data) => {
      qc.setQueryData(["me"], data);
      navigate(next, { replace: true });
    },
  });

  if (me.isPending) return <Spinner />;
  if (me.data) return <Navigate to={next} replace />;

  const cancelled = signIn.error instanceof ApiError && signIn.error.code === "passkey_cancelled";

  return (
    <AuthShell>
      <h1 className="text-lg font-semibold">Sign in</h1>
      <p className="mt-1 text-sm text-zinc-600">Use the passkey on this device or your phone.</p>
      <Button variant="primary" className="mt-5 w-full" disabled={signIn.isPending} onClick={() => signIn.mutate()}>
        <KeyRound className="size-4" />
        {signIn.isPending ? "Waiting for your passkey…" : "Sign in with passkey"}
      </Button>
      <div className="mt-4">
        {cancelled ? (
          <p className="text-sm text-zinc-600">Passkey prompt closed. Try again when you're ready.</p>
        ) : (
          <ErrorNote error={signIn.error} />
        )}
      </div>
      <p className="mt-5 border-t border-zinc-100 pt-4 text-xs text-zinc-500">
        No account? Ask an owner for an invite link from Team &amp; access.
      </p>
    </AuthShell>
  );
}
