import { useState } from "react";
import { Link, useParams } from "react-router";
import { LayoutTemplate, Lock, Plus, Trash2 } from "lucide-react";
import { useAllowDomain, useAllowlist, useDisallowDomain, useLayout, useTemplates } from "../../api/templates";
import { Badge, Button, Card, ErrorNote, inputClass, PageHeader, Spinner } from "../../components/ui";
import { timeAgo } from "../../lib/format";
import { IssueSummary, useBrandAccess } from "./parts";

// S13: the brand's templates, its locked layout and the domains its emails may link to.
export function TemplatesPage() {
  const slug = useParams().brand!;
  const { brand, canEdit } = useBrandAccess(slug);
  const templates = useTemplates(slug);

  return (
    <div className="max-w-5xl space-y-5">
      <PageHeader
        title="Templates"
        description={`Every ${brand?.name ?? ""} email is a template inside the brand's locked layout. Templates with errors can't be sent.`}
        action={
          canEdit && (
            <Link to="new">
              <Button variant="primary">
                <Plus className="size-4" /> New template
              </Button>
            </Link>
          )
        }
      />

      <Card>
        {templates.isPending ? (
          <div className="px-5">
            <Spinner />
          </div>
        ) : templates.isError ? (
          <div className="p-5">
            <ErrorNote error={templates.error} />
          </div>
        ) : templates.data.length === 0 ? (
          <p className="px-5 py-10 text-center text-sm text-zinc-500">
            No templates yet.{canEdit && " Start one with New template."}
          </p>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-zinc-200 bg-zinc-50 text-left text-[11px] tracking-wide text-zinc-500 uppercase">
                <th className="px-5 py-2 font-medium">Name</th>
                <th className="px-2 py-2 font-medium">Checks</th>
                <th className="px-2 py-2 pr-5 font-medium">Last edited</th>
              </tr>
            </thead>
            <tbody>
              {templates.data.map((t) => (
                <tr key={t.id} className="border-b border-zinc-100 last:border-0 hover:bg-zinc-50">
                  <td className="px-5 py-3">
                    <Link to={t.id} className="font-medium text-zinc-900 hover:underline">
                      {t.name}
                    </Link>
                  </td>
                  <td className="px-2 py-3">
                    {t.stale ? (
                      <Badge tone="blue" dot>
                        Re-checking…
                      </Badge>
                    ) : (
                      <IssueSummary errors={t.error_count} warnings={t.warning_count} />
                    )}
                  </td>
                  <td className="px-2 py-3 pr-5 text-zinc-600">
                    {timeAgo(t.updated_at)}
                    {t.updated_by_name && <> · {t.updated_by_name}</>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>

      <div className="grid gap-5 lg:grid-cols-2">
        <LayoutCard slug={slug} />
        <AllowlistCard slug={slug} />
      </div>
    </div>
  );
}

function LayoutCard({ slug }: { slug: string }) {
  const layout = useLayout(slug);
  const { canAdmin } = useBrandAccess(slug);
  const l = layout.data?.layout;
  return (
    <Card
      title={
        <span className="flex items-center gap-2">
          <Lock className="size-4 text-zinc-500" /> Brand layout
        </span>
      }
      description="Header, footer, unsubscribe link and postal address. The same for every email; only brand admins can change it."
      action={
        <Link to="layout">
          <Button size="sm">
            <LayoutTemplate className="size-3.5" /> {canAdmin ? "Edit layout" : "View layout"}
          </Button>
        </Link>
      }
    >
      <div className="border-t border-zinc-100 px-5 py-3 text-sm text-zinc-600">
        {layout.isPending ? (
          "Loading…"
        ) : layout.isError ? (
          <ErrorNote error={layout.error} />
        ) : l?.is_default ? (
          "Using the default layout."
        ) : (
          <>
            Version {l?.version} · changed {timeAgo(l?.updated_at)}
          </>
        )}
      </div>
    </Card>
  );
}

function AllowlistCard({ slug }: { slug: string }) {
  const list = useAllowlist(slug);
  const { canAdmin } = useBrandAccess(slug);
  const allow = useAllowDomain(slug);
  const disallow = useDisallowDomain(slug);
  const [domain, setDomain] = useState("");
  const result = allow.data ?? disallow.data;

  return (
    <Card
      title="Link allowlist"
      description="Emails may only link to these domains and the brand's own. Link shorteners are never allowed."
    >
      <div className="border-t border-zinc-100 px-5 py-3">
        {list.isPending ? (
          <Spinner />
        ) : list.isError ? (
          <ErrorNote error={list.error} />
        ) : (
          <ul className="flex flex-wrap gap-1.5" aria-label="Allowed domains">
            {list.data.brand_domains.map((d) => (
              <li key={d}>
                <Badge tone="indigo">{d} · brand</Badge>
              </li>
            ))}
            {list.data.domains.map((d) => (
              <li key={d.domain}>
                <span className="inline-flex items-center gap-1 rounded-full bg-zinc-100 py-0.5 pr-1 pl-2 text-xs font-medium text-zinc-700">
                  {d.domain}
                  {canAdmin && (
                    <button
                      type="button"
                      aria-label={`Remove ${d.domain}`}
                      onClick={() => disallow.mutate(d.domain)}
                      className="rounded-full p-0.5 text-zinc-400 hover:bg-zinc-200 hover:text-zinc-700"
                    >
                      <Trash2 className="size-3" />
                    </button>
                  )}
                </span>
              </li>
            ))}
          </ul>
        )}
        {canAdmin && (
          <form
            className="mt-3 flex gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              allow.mutate(domain.trim(), { onSuccess: () => setDomain("") });
            }}
          >
            <input
              aria-label="Domain to allow"
              placeholder="shop.example.com"
              className={inputClass}
              value={domain}
              onChange={(e) => setDomain(e.target.value)}
            />
            <Button type="submit" disabled={!domain.trim() || allow.isPending}>
              Allow
            </Button>
          </form>
        )}
        <div className="mt-2 space-y-2">
          <ErrorNote error={allow.error ?? disallow.error} />
          {result?.templates_queued ? (
            <p className="text-xs text-zinc-600">
              Re-checking {result.templates_queued} {result.templates_queued === 1 ? "template" : "templates"} in the background.
            </p>
          ) : null}
        </div>
      </div>
    </Card>
  );
}
