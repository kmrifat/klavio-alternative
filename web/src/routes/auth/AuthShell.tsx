import type { ReactNode } from "react";
import { Mail } from "lucide-react";
import { useCompanyName } from "../../api/hooks";

// Centered card used by the sign-in and invite pages.
export function AuthShell({ children }: { children: ReactNode }) {
  const companyName = useCompanyName();
  return (
    <div className="grid min-h-full place-items-center bg-zinc-50 px-4 py-12">
      <div className="w-full max-w-sm">
        <div className="mb-6 flex items-center justify-center gap-2">
          <span className="grid size-8 place-items-center rounded-lg bg-zinc-900 text-white">
            <Mail className="size-4" />
          </span>
          <span className="text-base font-semibold">{companyName}</span>
        </div>
        <div className="rounded-xl border border-zinc-200 bg-white p-6 shadow-sm">{children}</div>
        <p className="mt-4 text-center text-xs text-zinc-500">
          Passkeys only. One-time codes and SMS are not accepted.
        </p>
      </div>
    </div>
  );
}
