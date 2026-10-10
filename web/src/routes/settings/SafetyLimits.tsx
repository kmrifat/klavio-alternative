import { useState } from "react";
import { History, Info, Lock } from "lucide-react";
import { Link } from "react-router";
import { useChangeLimits, useSafetyLimits } from "../../api/governance";
import { useMe } from "../../api/hooks";
import type { SafetyLimit } from "../../api/types";
import { Button, Card, ErrorNote, Spinner, inputBase } from "../../components/ui";
import { timeAgo } from "../../lib/format";

const groups: { id: SafetyLimit["group"]; title: string; description: string }[] = [
  { id: "brakes", title: "Safety brakes", description: "When sending pauses automatically" },
  { id: "audience", title: "Audience & frequency", description: "Who can be emailed, and how often" },
  { id: "imports", title: "Imports", description: "What an import must pass before anyone can be emailed" },
];

function fmt(l: SafetyLimit, v: number) {
  return l.unit === "%" ? `${v}%` : `${v.toLocaleString()} ${l.unit}`;
}

// looser: does moving to v loosen the limit?
function looser(l: SafetyLimit, v: number) {
  return v !== l.value && v > l.value === l.looser_is_higher;
}

export function SafetyLimits() {
  const me = useMe().data!;
  const canEdit = me.user.company_role !== "member";
  const limits = useSafetyLimits();
  const change = useChangeLimits();
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [reason, setReason] = useState("");

  if (limits.isPending) return <Spinner />;
  if (limits.isError) return <ErrorNote error={limits.error} />;

  const changes: Record<string, number> = {};
  for (const [k, v] of Object.entries(draft)) {
    const l = limits.data.find((x) => x.key === k);
    const n = Number(v);
    if (l && v !== "" && !Number.isNaN(n) && n !== l.value) changes[k] = n;
  }
  const anyLooser = Object.entries(changes).some(([k, v]) => looser(limits.data.find((l) => l.key === k)!, v));
  const result = change.data;

  return (
    <div className="space-y-4">
      <p className="flex gap-2 rounded-lg border border-sky-200 bg-sky-50 px-4 py-3 text-sm text-sky-900">
        <Info className="mt-0.5 size-4 shrink-0" />
        <span>
          <strong>Tighten instantly, loosen with a second admin.</strong> Any admin can make a limit stricter right away. Making it looser needs
          another admin's approval, and nothing can go past the hard limit.
        </span>
      </p>

      {groups.map((g) => (
        <Card key={g.id} title={g.title} description={g.description}>
          <table className="w-full text-sm">
            <thead>
              <tr className="border-y border-zinc-200 bg-zinc-50 text-left text-[11px] tracking-wide text-zinc-500 uppercase">
                <th className="px-5 py-2 font-medium">Limit</th>
                <th className="px-3 py-2 text-right font-medium">Now</th>
                <th className="px-3 py-2 text-right font-medium">Hard limit</th>
                <th className="px-5 py-2 text-right font-medium">Changed</th>
              </tr>
            </thead>
            <tbody>
              {limits.data
                .filter((l) => l.group === g.id)
                .map((l) => {
                  const v = draft[l.key] ?? String(l.value);
                  const n = Number(v);
                  const isLooser = v !== "" && !Number.isNaN(n) && looser(l, n);
                  return (
                    <tr key={l.key} className="border-b border-zinc-100 last:border-0">
                      <td className="px-5 py-2.5">
                        <p>{l.label}</p>
                        {l.help && <p className="text-xs text-zinc-500">{l.help}</p>}
                      </td>
                      <td className="px-3 py-2.5 text-right">
                        <span className="inline-flex items-center gap-1">
                          <input
                            aria-label={l.label}
                            type="number"
                            step="any"
                            min="0"
                            disabled={!canEdit}
                            className={`${inputBase} w-24 text-right ${isLooser ? "border-amber-400" : ""}`}
                            value={v}
                            onChange={(e) => setDraft({ ...draft, [l.key]: e.target.value })}
                          />
                          <span className="w-12 text-left text-xs text-zinc-500">{l.unit}</span>
                        </span>
                        {isLooser && <p className="mt-0.5 text-xs text-amber-700">{l.locked ? "Locked — can't loosen" : "Needs approval"}</p>}
                      </td>
                      <td className="px-3 py-2.5 text-right">
                        {l.locked ? (
                          <span className="inline-flex items-center gap-1 text-accent">
                            <Lock className="size-3" /> Locked
                          </span>
                        ) : (
                          fmt(l, l.hard_limit)
                        )}
                      </td>
                      <td className="px-5 py-2.5 text-right text-zinc-500">
                        {l.changed_at ? `${l.changed_by || "someone"} · ${timeAgo(l.changed_at)}` : "Default"}
                      </td>
                    </tr>
                  );
                })}
            </tbody>
          </table>
        </Card>
      ))}

      {canEdit && (
        <div className="space-y-3">
          {anyLooser && (
            <input
              aria-label="Why loosen?"
              placeholder="Why loosen? The approver sees this."
              className={`${inputBase} w-full`}
              value={reason}
              onChange={(e) => setReason(e.target.value)}
            />
          )}
          <ErrorNote error={change.error} />
          {result && (
            <p role="status" className="rounded-md bg-green-50 px-3 py-2 text-sm text-green-800">
              {result.applied.length > 0 && <>{result.applied.length} change(s) applied. </>}
              {result.requested.length > 0 && (
                <>
                  {result.requested.length} change(s) sent for approval — see <Link to="/approvals" className="underline">Approvals</Link>.
                </>
              )}
            </p>
          )}
          <div className="flex justify-end gap-2">
            <Link to="/audit?action=safety_limit." className="inline-flex">
              <Button>
                <History className="size-4" /> Change history
              </Button>
            </Link>
            <Button
              variant="primary"
              disabled={Object.keys(changes).length === 0 || change.isPending}
              onClick={() => change.mutate({ changes, reason }, { onSuccess: () => setDraft({}) })}
            >
              {change.isPending ? "Saving…" : "Save changes"}
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
