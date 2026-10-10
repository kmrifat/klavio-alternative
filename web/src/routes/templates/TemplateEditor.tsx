import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { ArrowLeft, Archive } from "lucide-react";
import { ApiError } from "../../api/client";
import { previewTemplate, useArchiveTemplate, useLayout, useSaveTemplate, useTemplate, type Issue } from "../../api/templates";
import { Button, Card, ErrorNote, Field, inputClass, Spinner } from "../../components/ui";
import { timeAgo } from "../../lib/format";
import { NotFound } from "../NotFound";
import { EmailPreview, IssueList, IssueSummary, MergeTags, MJMLEditor, useBrandAccess, useLivePreview } from "./parts";

// The starter links to the brand's own site, which is always allowed.
export const starterTemplate = (site: string) => `<mj-section>
  <mj-column>
    <mj-text font-size="22px" font-weight="bold">Hello {{first_name | there}}</mj-text>
    <mj-text>
      Write your email here. Keep it short, say why the reader is getting it,
      and give them one clear thing to do.
    </mj-text>
    <mj-button href="https://${site}">Call to action</mj-button>
  </mj-column>
</mj-section>
`;

// Template editor (S13): MJML on the left, a live preview and the checks on
// the right. Saving is allowed with errors; sending is not.
export function TemplateEditor() {
  const { brand: slug, id } = useParams() as { brand: string; id?: string };
  const existing = useTemplate(slug, id);

  if (id && existing.isPending) return <Spinner />;
  if (id && existing.isError) {
    return existing.error instanceof ApiError && existing.error.status === 404 ? <NotFound /> : <ErrorNote error={existing.error} />;
  }
  // Remount when switching templates so the form starts from the right data.
  return <Editor key={id ?? "new"} slug={slug} id={id} loaded={existing.data} />;
}

function Editor({ slug, id, loaded }: { slug: string; id?: string; loaded?: ReturnType<typeof useTemplate>["data"] }) {
  const navigate = useNavigate();
  const { brand, canEdit } = useBrandAccess(slug);
  // news.brightside.co → brightside.co
  const [starter] = useState(() => starterTemplate(brand?.sending_domain.split(".").slice(1).join(".") || "example.com"));
  const tags = useLayout(slug).data?.tags ?? [];
  const save = useSaveTemplate(slug, id);
  const archive = useArchiveTemplate(slug);

  const t = loaded?.template;
  const [name, setName] = useState(t?.name ?? "");
  const [mjml, setMJML] = useState(t?.mjml ?? starter);
  const [version, setVersion] = useState(t?.version ?? 0);
  const dirty = name !== (t?.name ?? "") || mjml !== (t?.mjml ?? starter);

  // The saved result is shown until the first live preview of an edit comes back.
  const editing = canEdit && (dirty || !t);
  const live = useLivePreview(mjml, editing, (src) => previewTemplate(slug, src));
  const fresh = editing ? live.result : null;
  const issues: Issue[] = fresh?.issues ?? t?.issues ?? [];
  const html = fresh?.preview_html ?? loaded?.preview_html ?? "";
  const text = fresh?.preview_text ?? loaded?.preview_text ?? "";
  const errors = issues.filter((i) => i.severity === "error").length;

  useEffect(() => {
    if (!dirty) return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  const stale = save.error instanceof ApiError && save.error.code === "stale";

  return (
    <div className="space-y-4">
      <Link to={`/b/${slug}/templates`} className="inline-flex items-center gap-1 text-sm text-zinc-600 hover:text-zinc-900">
        <ArrowLeft className="size-4" /> Templates
      </Link>

      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate(
            { name, mjml, ...(id ? { version } : {}) },
            {
              onSuccess: (d) => {
                setVersion(d.template.version);
                if (!id) navigate(`/b/${slug}/templates/${d.template.id}`, { replace: true });
              },
            },
          );
        }}
      >
        <div className="flex flex-wrap items-end gap-3">
          <div className="min-w-64 flex-1">
            <Field label="Template name">
              {(fid) => (
                <input id={fid} required readOnly={!canEdit} className={inputClass} value={name} onChange={(e) => setName(e.target.value)} />
              )}
            </Field>
          </div>
          <div className="flex items-center gap-3 pb-1">
            <IssueSummary errors={errors} warnings={issues.length - errors} />
            {t && <span className="text-xs text-zinc-500">Saved {timeAgo(t.updated_at)}</span>}
          </div>
          {canEdit && (
            <div className="flex gap-2">
              {id && (
                <Button
                  variant="ghost"
                  disabled={archive.isPending}
                  onClick={() => {
                    if (window.confirm(`Archive “${t?.name}”? It disappears from the list and can't be picked for new campaigns.`)) {
                      archive.mutate(id, { onSuccess: () => navigate(`/b/${slug}/templates`) });
                    }
                  }}
                >
                  <Archive className="size-4" /> Archive
                </Button>
              )}
              <Button type="submit" variant="primary" disabled={!name.trim() || save.isPending || (!dirty && !!id)}>
                {save.isPending ? "Saving…" : id ? "Save" : "Create template"}
              </Button>
            </div>
          )}
        </div>
        {stale ? (
          <p role="alert" className="rounded-md bg-amber-50 px-3 py-2 text-sm text-amber-900">
            Someone else saved this template while you were editing. Copy your changes, then{" "}
            <button type="button" className="font-medium underline" onClick={() => window.location.reload()}>
              reload
            </button>{" "}
            to see theirs.
          </p>
        ) : (
          <ErrorNote error={save.error ?? archive.error} />
        )}

        <div className="grid gap-4 xl:grid-cols-2">
          <Card title="MJML" description="Body content only: mj-section and mj-wrapper blocks. The layout adds the header and footer.">
            <div className="border-t border-zinc-100 p-3">
              <MJMLEditor value={mjml} onChange={setMJML} readOnly={!canEdit} />
            </div>
            <MergeTags tags={tags} />
          </Card>

          <div className="space-y-4">
            <Card title="Checks" description={live.pending ? "Checking…" : "Errors block sending. Warnings are worth fixing."}>
              <div className="border-t border-zinc-100">
                <ErrorNote error={live.error} />
                <IssueList issues={issues} checking={live.pending} />
              </div>
            </Card>
            <Card title="Preview" description="Inside the brand layout, with sample data.">
              <div className="border-t border-zinc-100">
                <EmailPreview html={html} text={text} />
              </div>
            </Card>
          </div>
        </div>
      </form>
    </div>
  );
}
