import { useEffect, useRef, useState } from "react";
import { AlertTriangle, CheckCircle2, Monitor, Smartphone, Type, XCircle } from "lucide-react";
import { useBrands, useMe } from "../../api/hooks";
import type { BrandRole } from "../../api/types";
import type { Issue, MergeTag } from "../../api/templates";
import { Badge, CopyButton } from "../../components/ui";

const rank: Record<BrandRole, number> = { viewer: 1, editor: 2, sender: 3, brand_admin: 4 };

// useBrandAccess says what the signed-in user may do on a brand's templates;
// the server enforces the same rules.
export function useBrandAccess(slug: string) {
  const me = useMe().data;
  const brand = useBrands().data?.find((b) => b.slug === slug);
  const role: BrandRole | "" = me?.user.company_role === "owner" ? "brand_admin" : brand ? (me?.brand_roles[brand.id] ?? "") : "";
  const r = role ? rank[role] : 0;
  return { brand, canEdit: r >= rank.editor, canAdmin: r >= rank.brand_admin };
}

export function IssueSummary({ errors, warnings }: { errors: number; warnings: number }) {
  if (errors > 0) {
    return (
      <Badge tone="red" dot>
        {errors} {errors === 1 ? "error" : "errors"}
      </Badge>
    );
  }
  if (warnings > 0) {
    return (
      <Badge tone="amber" dot>
        {warnings} {warnings === 1 ? "warning" : "warnings"}
      </Badge>
    );
  }
  return (
    <Badge tone="green" dot>
      Ready
    </Badge>
  );
}

export function IssueList({ issues, checking }: { issues: Issue[]; checking?: boolean }) {
  if (issues.length === 0) {
    return (
      <p className="flex items-center gap-2 px-5 py-4 text-sm text-green-700">
        <CheckCircle2 className="size-4" /> {checking ? "Checking…" : "Passes every check."}
      </p>
    );
  }
  const sorted = [...issues].sort((a, b) => (a.severity === b.severity ? 0 : a.severity === "error" ? -1 : 1));
  return (
    <ul aria-label="Check results" className="divide-y divide-zinc-100">
      {sorted.map((is, i) => (
        <li key={i} className="flex gap-2.5 px-5 py-2.5 text-sm">
          {is.severity === "error" ? (
            <XCircle className="mt-0.5 size-4 shrink-0 text-red-600" aria-label="Error" />
          ) : (
            <AlertTriangle className="mt-0.5 size-4 shrink-0 text-amber-600" aria-label="Warning" />
          )}
          <span>
            {is.message}
            {is.line ? (
              <span className="ml-1.5 text-xs text-zinc-500">
                ({is.where === "layout" ? "layout " : ""}line {is.line})
              </span>
            ) : null}
          </span>
        </li>
      ))}
    </ul>
  );
}

type View = "desktop" | "mobile" | "text";

// EmailPreview shows compiled HTML in a sandboxed frame: no scripts, no
// navigation, nothing from the email can touch the app.
export function EmailPreview({ html, text }: { html: string; text?: string }) {
  const [view, setView] = useState<View>("desktop");
  const views: { id: View; label: string; icon: typeof Monitor }[] = [
    { id: "desktop", label: "Desktop", icon: Monitor },
    { id: "mobile", label: "Mobile", icon: Smartphone },
    ...(text !== undefined ? [{ id: "text" as View, label: "Plain text", icon: Type }] : []),
  ];
  return (
    <div className="flex h-full min-h-[520px] flex-col">
      <div role="tablist" aria-label="Preview size" className="flex gap-1 border-b border-zinc-100 px-3 py-2">
        {views.map((v) => (
          <button
            key={v.id}
            type="button"
            role="tab"
            aria-selected={view === v.id}
            onClick={() => setView(v.id)}
            className={`inline-flex items-center gap-1.5 rounded-md px-2.5 py-1 text-xs font-medium ${
              view === v.id ? "bg-zinc-900 text-white" : "text-zinc-600 hover:bg-zinc-100"
            }`}
          >
            <v.icon className="size-3.5" /> {v.label}
          </button>
        ))}
      </div>
      <div className="flex flex-1 justify-center overflow-auto bg-zinc-100 p-4">
        {view === "text" ? (
          <pre className="w-full max-w-2xl rounded-lg bg-white p-5 font-mono text-xs whitespace-pre-wrap text-zinc-800">{text}</pre>
        ) : html ? (
          <iframe
            title="Email preview"
            sandbox=""
            srcDoc={html}
            className={`h-full min-h-[480px] rounded-lg border border-zinc-200 bg-white ${view === "mobile" ? "w-[375px]" : "w-full max-w-[680px]"}`}
          />
        ) : (
          <p className="self-center text-sm text-zinc-500">Fix the errors to see a preview.</p>
        )}
      </div>
    </div>
  );
}

export function MergeTags({ tags, layout }: { tags: MergeTag[]; layout?: boolean }) {
  const all = layout ? [{ name: "content", description: "Where each template goes (exactly once)", url: false }, ...tags] : tags;
  return (
    <details className="px-5 py-3 text-sm">
      <summary className="cursor-pointer font-medium text-zinc-700">Merge tags</summary>
      <p className="mt-2 text-xs text-zinc-500">
        Filled in for each person when the email is sent. Add a fallback after a bar: <code>{"{{first_name | there}}"}</code>.
      </p>
      <ul className="mt-2 space-y-1">
        {all.map((t) => (
          <li key={t.name} className="flex items-center gap-2">
            <code className="rounded bg-zinc-100 px-1.5 py-0.5 text-xs">{`{{${t.name}}}`}</code>
            <CopyButton text={`{{${t.name}}}`} label="Copy merge tag" />
            <span className="text-xs text-zinc-500">{t.description}</span>
          </li>
        ))}
      </ul>
    </details>
  );
}

// MJMLEditor is a plain monospace textarea; Tab indents instead of leaving.
export function MJMLEditor({ id, value, onChange, readOnly, rows = 26 }: {
  id?: string;
  value: string;
  onChange: (v: string) => void;
  readOnly?: boolean;
  rows?: number;
}) {
  return (
    <textarea
      id={id}
      aria-label="MJML source"
      value={value}
      readOnly={readOnly}
      rows={rows}
      spellCheck={false}
      autoCapitalize="off"
      autoComplete="off"
      onChange={(e) => onChange(e.target.value)}
      onKeyDown={(e) => {
        if (e.key !== "Tab" || readOnly) return;
        e.preventDefault();
        const t = e.currentTarget;
        const { selectionStart: s, selectionEnd: end } = t;
        onChange(t.value.slice(0, s) + "  " + t.value.slice(end));
        requestAnimationFrame(() => t.setSelectionRange(s + 2, s + 2));
      }}
      className="block w-full resize-y rounded-md border border-zinc-200 bg-zinc-950 p-3 font-mono text-xs leading-5 text-zinc-100 outline-none focus:border-accent focus:ring-2 focus:ring-accent/20"
    />
  );
}

// useLivePreview recompiles source a moment after typing stops. Answers to
// older requests are dropped, so a slow one never overwrites a newer one.
export function useLivePreview<T>(source: string, enabled: boolean, run: (src: string) => Promise<T>, delay = 500) {
  const [result, setResult] = useState<T | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [pending, setPending] = useState(false);
  const seq = useRef(0);
  const runRef = useRef(run);
  runRef.current = run;

  useEffect(() => {
    if (!enabled) return;
    const n = ++seq.current;
    setPending(true);
    const t = setTimeout(() => {
      runRef.current(source).then(
        (r) => {
          if (n !== seq.current) return;
          setResult(r);
          setError(null);
          setPending(false);
        },
        (e: unknown) => {
          if (n !== seq.current) return;
          setError(e);
          setPending(false);
        },
      );
    }, delay);
    return () => clearTimeout(t);
  }, [source, enabled, delay]);

  return { result, error, pending };
}
