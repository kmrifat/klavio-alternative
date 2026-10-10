import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { ArrowLeft, Filter, Plus, Trash2, X } from "lucide-react";
import { useTopics } from "../../api/contacts";
import { useBrands, useMe } from "../../api/hooks";
import {
  opLabel,
  useDeleteSegment,
  useSaveSegment,
  useSegment,
  useSegmentFields,
  useSegmentPreview,
  useSegments,
  type Condition,
  type RuleGroup,
  type Rules,
  type SegmentField,
} from "../../api/segments";
import type { Topic } from "../../api/types";
import { Badge, Button, Card, ErrorNote, PageHeader, Spinner, inputBase } from "../../components/ui";
import { tierInfo, timeAgo } from "../../lib/format";

function useCanEdit(slug: string) {
  const me = useMe().data!;
  const brand = useBrands().data?.find((b) => b.slug === slug);
  const role = me.user.company_role === "owner" ? "brand_admin" : (brand?.my_role ?? "");
  return { canEdit: role === "editor" || role === "sender" || role === "brand_admin" };
}

// S12: saved segments for this brand.
export function SegmentsPage() {
  const slug = useParams().brand!;
  const segments = useSegments(slug);
  const { canEdit } = useCanEdit(slug);
  return (
    <div className="max-w-5xl space-y-5">
      <PageHeader
        title="Segments"
        description="Saved groups of contacts that update themselves. Unsafe people (unsubscribed, suppressed, sunset, over the frequency cap) are still left out when you send."
        action={
          canEdit && (
            <Link to="new">
              <Button variant="primary">
                <Plus className="size-4" /> New segment
              </Button>
            </Link>
          )
        }
      />
      {segments.isPending ? (
        <Spinner />
      ) : segments.isError ? (
        <ErrorNote error={segments.error} />
      ) : segments.data.length === 0 ? (
        <div className="flex items-start gap-3 rounded-xl border border-dashed border-zinc-300 bg-zinc-50 p-5 text-sm text-zinc-600">
          <Filter className="mt-0.5 size-5 text-zinc-400" />
          <p>No segments yet. A segment is a rule like “Active or Warm, in Germany, tagged vip”.</p>
        </div>
      ) : (
        <Card>
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-zinc-200 bg-zinc-50 text-left text-[11px] tracking-wide text-zinc-500 uppercase">
                <th className="px-5 py-2 font-medium">Name</th>
                <th className="px-3 py-2 text-right font-medium">Matching</th>
                <th className="px-3 py-2 text-right font-medium">Can be emailed</th>
                <th className="px-5 py-2 text-right font-medium">Updated</th>
              </tr>
            </thead>
            <tbody>
              {segments.data.map((s) => (
                <tr key={s.id} className="border-b border-zinc-100 last:border-0 hover:bg-zinc-50">
                  <td className="px-5 py-2.5">
                    <Link to={s.id} className="font-medium hover:underline">
                      {s.name}
                    </Link>
                  </td>
                  <td className="px-3 py-2.5 text-right tabular-nums">{s.counts.matching.toLocaleString()}</td>
                  <td className="px-3 py-2.5 text-right tabular-nums">{s.counts.mailable.toLocaleString()}</td>
                  <td className="px-5 py-2.5 text-right text-zinc-500">{timeAgo(s.updated_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </Card>
      )}
    </div>
  );
}

const emptyRules: Rules = { match: "all", groups: [{ match: "all", conditions: [{ field: "tier", op: "in", value: ["active", "warm"] }] }] };

// SegmentEditor: build rules with a live count (S12).
export function SegmentEditor() {
  const slug = useParams().brand!;
  const id = useParams().id;
  const isNew = !id;
  const existing = useSegment(slug, id);
  if (!isNew && existing.isPending) return <Spinner />;
  if (!isNew && existing.isError) return <ErrorNote error={existing.error} />;
  return <Editor key={id ?? "new"} slug={slug} id={id} initialName={existing.data?.name ?? ""} initialRules={existing.data?.rules ?? emptyRules} />;
}

function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return v;
}

function Editor({ slug, id, initialName, initialRules }: { slug: string; id?: string; initialName: string; initialRules: Rules }) {
  const navigate = useNavigate();
  const fields = useSegmentFields();
  const topics = useTopics(slug).data ?? [];
  const { canEdit } = useCanEdit(slug);
  const [name, setName] = useState(initialName);
  const [rules, setRules] = useState<Rules>(initialRules);
  const debounced = useDebounced(rules, 400);
  const preview = useSegmentPreview(slug, debounced);
  const save = useSaveSegment(slug, id);
  const del = useDeleteSegment(slug);

  if (fields.isPending) return <Spinner />;
  if (fields.isError) return <ErrorNote error={fields.error} />;
  const fieldList = fields.data;

  const setGroup = (gi: number, g: RuleGroup) => setRules({ ...rules, groups: rules.groups.map((x, i) => (i === gi ? g : x)) });
  const removeGroup = (gi: number) => setRules({ ...rules, groups: rules.groups.filter((_, i) => i !== gi) });
  const counts = preview.data?.counts;

  return (
    <div className="max-w-5xl space-y-5">
      <Link to={`/b/${slug}/segments`} className="inline-flex items-center gap-1 text-sm text-zinc-600 hover:text-zinc-900">
        <ArrowLeft className="size-4" /> Segments
      </Link>
      <PageHeader title={id ? "Edit segment" : "New segment"} />

      <div className="grid gap-5 lg:grid-cols-[1fr_280px]">
        <div className="space-y-4">
          <input
            aria-label="Segment name"
            placeholder="Segment name, e.g. Engaged in Germany"
            className={`${inputBase} w-full text-base`}
            value={name}
            disabled={!canEdit}
            onChange={(e) => setName(e.target.value)}
          />

          <div className="flex items-center gap-2 text-sm">
            Contacts who match
            <select
              aria-label="Groups match"
              className={inputBase}
              value={rules.match}
              disabled={!canEdit}
              onChange={(e) => setRules({ ...rules, match: e.target.value as Rules["match"] })}
            >
              <option value="all">all</option>
              <option value="any">any</option>
            </select>
            of these groups
          </div>

          {rules.groups.map((g, gi) => (
            <GroupEditor
              key={gi}
              index={gi}
              group={g}
              fields={fieldList}
              topics={topics}
              disabled={!canEdit}
              onChange={(ng) => setGroup(gi, ng)}
              onRemove={rules.groups.length > 1 ? () => removeGroup(gi) : undefined}
            />
          ))}
          {canEdit && (
            <Button
              size="sm"
              onClick={() => setRules({ ...rules, groups: [...rules.groups, { match: "all", conditions: [{ field: "tier", op: "is", value: "active" }] }] })}
            >
              <Plus className="size-3.5" /> Add group
            </Button>
          )}

          <ErrorNote error={save.error ?? del.error} />
          {canEdit && (
            <div className="flex justify-between gap-2 pt-2">
              {id ? (
                <Button
                  variant="ghost"
                  onClick={() => {
                    if (confirm(`Delete “${name}”?`)) del.mutate(id, { onSuccess: () => navigate(`/b/${slug}/segments`) });
                  }}
                >
                  <Trash2 className="size-4" /> Delete
                </Button>
              ) : (
                <span />
              )}
              <Button
                variant="primary"
                disabled={!name.trim() || save.isPending}
                onClick={() => save.mutate({ name, rules }, { onSuccess: (s) => navigate(`/b/${slug}/segments/${s.id}`, { replace: true }) })}
              >
                {save.isPending ? "Saving…" : "Save segment"}
              </Button>
            </div>
          )}
        </div>

        <aside className="space-y-3">
          <Card title="Live count">
            <div className="space-y-3 border-t border-zinc-200 px-5 py-4" aria-live="polite">
              {preview.isError ? (
                <ErrorNote error={preview.error} />
              ) : !counts ? (
                <Spinner label="Counting…" />
              ) : (
                <>
                  <div>
                    <p className="text-3xl font-semibold tabular-nums" aria-label="Matching contacts">
                      {counts.matching.toLocaleString()}
                    </p>
                    <p className="text-xs text-zinc-500">contacts match</p>
                  </div>
                  <div>
                    <p className="text-xl font-semibold tabular-nums" aria-label="Can be emailed">
                      {counts.mailable.toLocaleString()}
                    </p>
                    <p className="text-xs text-zinc-500">can be emailed today (subscribed, not suppressed, not sunset)</p>
                  </div>
                  <ul className="space-y-1 text-xs">
                    {(Object.keys(tierInfo) as (keyof typeof tierInfo)[]).map((t) => (
                      <li key={t} className="flex justify-between">
                        <Badge tone={tierInfo[t].tone}>{tierInfo[t].label}</Badge>
                        <span className="tabular-nums">{(counts.tiers[t] ?? 0).toLocaleString()}</span>
                      </li>
                    ))}
                  </ul>
                </>
              )}
            </div>
          </Card>
          {preview.data && preview.data.sample.length > 0 && (
            <Card title="First matches">
              <ul className="divide-y divide-zinc-100 border-t border-zinc-200 text-sm">
                {preview.data.sample.map((c) => (
                  <li key={c.id} className="truncate px-5 py-1.5">
                    {c.email}
                  </li>
                ))}
              </ul>
            </Card>
          )}
        </aside>
      </div>
    </div>
  );
}

function GroupEditor({ index, group, fields, topics, disabled, onChange, onRemove }: {
  index: number;
  group: RuleGroup;
  fields: SegmentField[];
  topics: Topic[];
  disabled: boolean;
  onChange: (g: RuleGroup) => void;
  onRemove?: () => void;
}) {
  const setCond = (ci: number, c: Condition) => onChange({ ...group, conditions: group.conditions.map((x, i) => (i === ci ? c : x)) });
  return (
    <section aria-label={`Group ${index + 1}`} className="rounded-xl border border-zinc-200 bg-white p-4">
      <div className="mb-3 flex items-center gap-2 text-sm">
        <span className="font-medium">Group {index + 1}:</span> match
        <select
          aria-label={`Group ${index + 1} match`}
          className={inputBase}
          value={group.match}
          disabled={disabled}
          onChange={(e) => onChange({ ...group, match: e.target.value as RuleGroup["match"] })}
        >
          <option value="all">all</option>
          <option value="any">any</option>
        </select>
        of
        {onRemove && !disabled && (
          <button type="button" aria-label={`Remove group ${index + 1}`} onClick={onRemove} className="ml-auto rounded p-1 text-zinc-400 hover:bg-zinc-100">
            <X className="size-4" />
          </button>
        )}
      </div>
      <div className="space-y-2">
        {group.conditions.map((c, ci) => (
          <ConditionRow
            key={ci}
            label={`Group ${index + 1} condition ${ci + 1}`}
            cond={c}
            fields={fields}
            topics={topics}
            disabled={disabled}
            onChange={(nc) => setCond(ci, nc)}
            onRemove={group.conditions.length > 1 ? () => onChange({ ...group, conditions: group.conditions.filter((_, i) => i !== ci) }) : undefined}
          />
        ))}
      </div>
      {!disabled && (
        <Button size="sm" variant="ghost" className="mt-2" onClick={() => onChange({ ...group, conditions: [...group.conditions, { field: "country", op: "is", value: "" }] })}>
          <Plus className="size-3.5" /> Add condition
        </Button>
      )}
    </section>
  );
}

// defaultValue for a field + operator pair.
function defaultValue(f: SegmentField, op: string): unknown {
  if (op === "is_empty" || op === "is_not_empty") return undefined;
  if (op === "in" || op === "not_in") return f.kind === "enum" ? [f.options![0]] : [];
  if (op === "within_days" || op === "older_than_days") return 30;
  if (f.kind === "enum") return f.options![0];
  return "";
}

function ConditionRow({ label, cond, fields, topics, disabled, onChange, onRemove }: {
  label: string;
  cond: Condition;
  fields: SegmentField[];
  topics: Topic[];
  disabled: boolean;
  onChange: (c: Condition) => void;
  onRemove?: () => void;
}) {
  const f = fields.find((x) => x.key === cond.field) ?? fields[0]!;
  return (
    <div className="flex flex-wrap items-center gap-2" role="group" aria-label={label}>
      <select
        aria-label={`${label} field`}
        className={inputBase}
        value={f.key}
        disabled={disabled}
        onChange={(e) => {
          const nf = fields.find((x) => x.key === e.target.value)!;
          const op = nf.ops[0]!;
          onChange({ field: nf.key, op, value: defaultValue(nf, op) });
        }}
      >
        {fields.map((x) => (
          <option key={x.key} value={x.key}>
            {x.label}
          </option>
        ))}
      </select>
      <select
        aria-label={`${label} operator`}
        className={inputBase}
        value={cond.op}
        disabled={disabled}
        onChange={(e) => onChange({ field: f.key, op: e.target.value, value: defaultValue(f, e.target.value) })}
      >
        {f.ops.map((op) => (
          <option key={op} value={op}>
            {opLabel[op] ?? op}
          </option>
        ))}
      </select>
      <ValueInput label={`${label} value`} f={f} cond={cond} topics={topics} disabled={disabled} onChange={(value) => onChange({ ...cond, value })} />
      {onRemove && !disabled && (
        <button type="button" aria-label={`Remove ${label}`} onClick={onRemove} className="rounded p-1 text-zinc-400 hover:bg-zinc-100">
          <X className="size-4" />
        </button>
      )}
    </div>
  );
}

function ValueInput({ label, f, cond, topics, disabled, onChange }: {
  label: string;
  f: SegmentField;
  cond: Condition;
  topics: Topic[];
  disabled: boolean;
  onChange: (v: unknown) => void;
}) {
  const op = cond.op;
  if (op === "is_empty" || op === "is_not_empty") return null;
  const common = { "aria-label": label, disabled, className: `${inputBase} min-w-40 flex-1` };
  if (op === "within_days" || op === "older_than_days") {
    return (
      <span className="flex items-center gap-1 text-sm">
        <input {...common} className={`${inputBase} w-24`} type="number" min={1} max={3650} value={Number(cond.value ?? 30)} onChange={(e) => onChange(Number(e.target.value))} />
        days
      </span>
    );
  }
  if (f.kind === "date") return <input {...common} type="date" value={String(cond.value ?? "")} onChange={(e) => onChange(e.target.value)} />;
  if (f.kind === "topic") {
    return (
      <select {...common} value={String(cond.value ?? "")} onChange={(e) => onChange(e.target.value)}>
        <option value="">Pick a topic…</option>
        {topics.map((t) => (
          <option key={t.id} value={t.id}>
            {t.name}
          </option>
        ))}
      </select>
    );
  }
  if (f.kind === "enum" && (op === "in" || op === "not_in")) {
    const vals = Array.isArray(cond.value) ? (cond.value as string[]) : [];
    return (
      <span className="flex flex-wrap gap-2 text-sm">
        {f.options!.map((o) => (
          <label key={o} className="inline-flex items-center gap-1">
            <input
              type="checkbox"
              disabled={disabled}
              aria-label={`${label} ${o}`}
              checked={vals.includes(o)}
              onChange={(e) => onChange(e.target.checked ? [...vals, o] : vals.filter((v) => v !== o))}
            />
            {optionLabel(o)}
          </label>
        ))}
      </span>
    );
  }
  if (f.kind === "enum") {
    return (
      <select {...common} value={String(cond.value ?? "")} onChange={(e) => onChange(e.target.value)}>
        {f.options!.map((o) => (
          <option key={o} value={o}>
            {optionLabel(o)}
          </option>
        ))}
      </select>
    );
  }
  if (op === "in" || op === "not_in") {
    return <ListInput common={common} placeholder={f.kind === "country" ? "DE, AT, CH" : "comma-separated"} value={cond.value} onChange={onChange} />;
  }
  return (
    <input
      {...common}
      placeholder={f.kind === "country" ? "DE" : f.kind === "tag" ? "tag" : "value"}
      value={String(cond.value ?? "")}
      onChange={(e) => onChange(e.target.value)}
    />
  );
}

// ListInput keeps the typed text (so "DE, " can be typed) and reports the list.
function ListInput({ common, placeholder, value, onChange }: {
  common: { "aria-label": string; disabled: boolean; className: string };
  placeholder: string;
  value: unknown;
  onChange: (v: string[]) => void;
}) {
  const [text, setText] = useState(Array.isArray(value) ? (value as string[]).join(", ") : "");
  return (
    <input
      {...common}
      placeholder={placeholder}
      value={text}
      onChange={(e) => {
        setText(e.target.value);
        onChange(
          e.target.value
            .split(",")
            .map((s) => s.trim())
            .filter(Boolean),
        );
      }}
    />
  );
}

function optionLabel(o: string) {
  return (tierInfo as Record<string, { label: string }>)[o]?.label ?? o.replaceAll("_", " ");
}
