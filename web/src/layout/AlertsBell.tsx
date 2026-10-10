import { useEffect, useRef, useState } from "react";
import { Bell } from "lucide-react";
import { useAcknowledgeAlert, useAlerts } from "../api/governance";
import { useMe } from "../api/hooks";
import { timeAgo } from "../lib/format";

const dot = { info: "bg-sky-500", warning: "bg-amber-500", critical: "bg-red-500" };

// Anomaly alerts for owners and approvers (S9): new country/device, mass exports, import spikes.
export function AlertsBell() {
  const me = useMe().data;
  const admin = !!me && me.user.company_role !== "member";
  const alerts = useAlerts(admin);
  const ack = useAcknowledgeAlert();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => !ref.current?.contains(e.target as Node) && setOpen(false);
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [open]);

  if (!admin) return <Bell className="size-4 text-zinc-400" aria-hidden />;
  const list = alerts.data ?? [];
  const critical = list.some((a) => a.severity === "critical");
  return (
    <div ref={ref} className="relative">
      <button
        type="button"
        aria-label={list.length ? `${list.length} open alerts` : "No open alerts"}
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className="relative rounded p-1 text-zinc-600 hover:bg-zinc-100"
      >
        <Bell className="size-4" />
        {list.length > 0 && <span className={`absolute top-0.5 right-0.5 size-2 rounded-full ${critical ? "bg-red-500" : "bg-amber-500"}`} />}
      </button>
      {open && (
        <div role="dialog" aria-label="Alerts" className="absolute top-full right-0 z-20 mt-2 w-96 rounded-xl border border-zinc-200 bg-white p-2 shadow-xl">
          <p className="px-2 py-1 text-xs font-medium tracking-wide text-zinc-500 uppercase">Open alerts</p>
          {list.length === 0 ? (
            <p className="px-2 py-4 text-sm text-zinc-500">Nothing unusual.</p>
          ) : (
            <ul>
              {list.map((a) => (
                <li key={a.id} className="flex items-start gap-2 rounded-lg px-2 py-2 hover:bg-zinc-50">
                  <span className={`mt-1.5 size-2 shrink-0 rounded-full ${dot[a.severity]}`} />
                  <div className="min-w-0 flex-1">
                    <p className="text-sm">{a.message}</p>
                    <p className="text-xs text-zinc-500">{timeAgo(a.created_at)}</p>
                  </div>
                  <button type="button" className="text-xs text-accent hover:underline" onClick={() => ack.mutate(a.id)}>
                    Dismiss
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}
