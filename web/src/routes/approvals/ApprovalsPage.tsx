import { useState } from "react";
import { Check, Download, Hourglass, X } from "lucide-react";
import { downloadExport, useApprovals, useCancelApproval, useDecide, useMyExports, type ApprovalView } from "../../api/governance";
import type { ApprovalItem, ExportItem } from "../../api/types";
import { Badge, Button, Card, ErrorNote, PageHeader, Spinner, inputClass } from "../../components/ui";
import { timeAgo } from "../../lib/format";

const statusTone = {
  pending: "amber",
  approved: "green",
  rejected: "red",
  cancelled: "zinc",
  expired: "zinc",
} as const;

export function ApprovalsPage() {
  const [view, setView] = useState<ApprovalView>("decide");
  const waiting = useApprovals("decide");
  const list = useApprovals(view);
  const tabs: { id: ApprovalView; label: string }[] = [
    { id: "decide", label: `Waiting for you${waiting.data ? ` (${waiting.data.length})` : ""}` },
    { id: "mine", label: "Your requests" },
    { id: "recent", label: "All recent" },
  ];

  return (
    <div className="max-w-5xl space-y-5">
      <PageHeader
        title="Approvals"
        description="Requests waiting for a second person. Nobody approves their own, except sends their AI agent prepared; approving asks for your passkey; everything is logged."
      />
      <div role="tablist" className="flex gap-1 border-b border-zinc-200">
        {tabs.map((t) => (
          <button
            key={t.id}
            role="tab"
            aria-selected={view === t.id}
            onClick={() => setView(t.id)}
            className={`-mb-px border-b-2 px-3 py-2 text-sm ${view === t.id ? "border-accent font-medium text-zinc-900" : "border-transparent text-zinc-600 hover:text-zinc-900"}`}
          >
            {t.label}
          </button>
        ))}
      </div>

      {view === "mine" && <MyExports />}

      {list.isPending ? (
        <Spinner />
      ) : list.isError ? (
        <ErrorNote error={list.error} />
      ) : list.data.length === 0 ? (
        <p className="rounded-xl border border-dashed border-zinc-300 px-5 py-10 text-center text-sm text-zinc-500">
          {view === "decide" ? "Nothing is waiting for you." : "No requests yet."}
        </p>
      ) : (
        <ul className="space-y-3">
          {list.data.map((a) => (
            <ApprovalCard key={a.id} a={a} />
          ))}
        </ul>
      )}
    </div>
  );
}

function ApprovalCard({ a }: { a: ApprovalItem }) {
  const decide = useDecide();
  const cancel = useCancelApproval();
  const [rejecting, setRejecting] = useState(false);
  const [note, setNote] = useState("");

  return (
    <li className="rounded-xl border border-zinc-200 bg-white p-4" aria-label={a.summary}>
      <div className="flex flex-wrap items-start gap-3">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <Badge tone="indigo">{a.label}</Badge>
            {a.brand_name && <Badge>{a.brand_name}</Badge>}
            <Badge tone={statusTone[a.status]} dot>
              {a.status}
            </Badge>
          </div>
          <p className="mt-2 font-medium">{a.summary}</p>
          {a.reason && <p className="mt-0.5 text-sm text-zinc-600">“{a.reason}”</p>}
          {a.detail && <p className="mt-0.5 text-sm text-amber-800">{a.detail}</p>}
          <p className="mt-1 text-xs text-zinc-500">
            Requested by {a.mine ? "you" : a.requester_name} · {timeAgo(a.requested_at)}
            {a.status === "pending" && <> · expires {new Date(a.expires_at).toLocaleDateString()}</>}
            {a.decided_at && a.decider_name && (
              <>
                {" "}
                · {a.status} by {a.decider_name} {timeAgo(a.decided_at)}
                {a.decision_note && <> — “{a.decision_note}”</>}
              </>
            )}
          </p>
        </div>
        {a.can_decide && !rejecting && (
          <div className="flex gap-2">
            <Button size="sm" onClick={() => setRejecting(true)}>
              <X className="size-3.5" /> Reject
            </Button>
            <Button size="sm" variant="primary" disabled={decide.isPending} onClick={() => decide.mutate({ id: a.id, decision: "approve", note: "" })}>
              <Check className="size-3.5" /> Approve
            </Button>
          </div>
        )}
        {a.mine && a.status === "pending" && (
          <div className="flex items-center gap-2 text-xs text-zinc-500">
            {!a.can_decide && (
              <>
                <Hourglass className="size-3.5" /> Waiting for someone else
              </>
            )}
            <Button size="sm" variant="ghost" disabled={cancel.isPending} onClick={() => cancel.mutate(a.id)}>
              Cancel request
            </Button>
          </div>
        )}
      </div>
      {rejecting && (
        <form
          className="mt-3 flex gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            decide.mutate({ id: a.id, decision: "reject", note }, { onSuccess: () => setRejecting(false) });
          }}
        >
          <input aria-label="Why reject?" required placeholder="Why? The requester sees this." className={inputClass} value={note} onChange={(e) => setNote(e.target.value)} />
          <Button type="submit" variant="danger" disabled={decide.isPending}>
            Reject
          </Button>
          <Button onClick={() => setRejecting(false)}>Back</Button>
        </form>
      )}
      <div className="mt-2">
        <ErrorNote error={decide.error ?? cancel.error} />
      </div>
    </li>
  );
}

function MyExports() {
  const exports = useMyExports();
  const [error, setError] = useState<unknown>(null);
  if (!exports.data || exports.data.length === 0) return null;
  const label: Record<ExportItem["status"], string> = {
    waiting: "Waiting for approval",
    ready: "Ready",
    expired: "Expired",
    rejected: "Rejected",
    cancelled: "Cancelled",
  };
  return (
    <Card title="Your exports" description="Downloadable for 24 hours after approval, by you only. Files are watermarked with your name.">
      <ul>
        {exports.data.map((e) => (
          <li key={e.id} className="flex items-center gap-3 border-t border-zinc-100 px-5 py-2.5 text-sm">
            <span className="flex-1">
              {e.brand_name} · {e.row_count.toLocaleString()} contacts · {timeAgo(e.created_at)}
            </span>
            <Badge tone={e.status === "ready" ? "green" : e.status === "waiting" ? "amber" : "zinc"}>{label[e.status]}</Badge>
            {e.status === "ready" && (
              <Button size="sm" onClick={() => downloadExport(e.id).then(() => void exports.refetch(), setError)}>
                <Download className="size-3.5" /> Download
              </Button>
            )}
          </li>
        ))}
      </ul>
      {error != null && (
        <div className="px-5 pb-3">
          <ErrorNote error={error} />
        </div>
      )}
    </Card>
  );
}
