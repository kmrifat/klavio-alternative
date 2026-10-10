import { useState } from "react";
import { Link, useParams } from "react-router";
import { ArrowLeft, Lock } from "lucide-react";
import { ApiError } from "../../api/client";
import { previewLayout, useLayout, useSaveLayout, type Issue, type Layout, type LayoutResponse } from "../../api/templates";
import { Button, Card, ErrorNote, Spinner } from "../../components/ui";
import { timeAgo } from "../../lib/format";
import { EmailPreview, IssueList, MergeTags, MJMLEditor, useBrandAccess, useLivePreview } from "./parts";

// Brand layout editor (S13). The layout wraps every email the brand sends,
// so it can't be saved with any check failing, and only brand admins can
// change it (with a passkey confirmation).
export function LayoutEditor() {
  const slug = useParams().brand!;
  const layout = useLayout(slug);
  // Kept out of Editor: a save bumps the version, which remounts Editor.
  const save = useSaveLayout(slug);
  if (layout.isPending) return <Spinner />;
  if (layout.isError) return <ErrorNote error={layout.error} />;
  return <Editor key={layout.data.layout.version} slug={slug} saved={layout.data} save={save} />;
}

function Editor({ slug, saved, save }: { slug: string; saved: LayoutResponse; save: ReturnType<typeof useSaveLayout> }) {
  const { canAdmin } = useBrandAccess(slug);
  const l: Layout = saved.layout;
  const [mjml, setMJML] = useState(l.mjml);
  const dirty = mjml !== l.mjml;

  const live = useLivePreview(mjml, canAdmin && dirty, (src) => previewLayout(slug, src));
  const fresh = dirty ? live.result : null;
  // A refused save carries the issues that blocked it.
  const issues: Issue[] = fresh?.issues ?? saved.issues;
  const errors = issues.some((i) => i.severity === "error");

  return (
    <div className="space-y-4">
      <Link to={`/b/${slug}/templates`} className="inline-flex items-center gap-1 text-sm text-zinc-600 hover:text-zinc-900">
        <ArrowLeft className="size-4" /> Templates
      </Link>

      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="flex items-center gap-2 text-2xl font-semibold tracking-tight">
            <Lock className="size-5 text-zinc-500" /> Brand layout
          </h1>
          <p className="mt-1 max-w-2xl text-sm text-zinc-600">
            {l.is_default ? "Default layout" : `Version ${l.version}, changed ${timeAgo(l.updated_at)}`}. Every template is
            compiled inside it, so it must keep {"{{content}}"}, the unsubscribe link and the postal address. Saving recompiles
            and re-checks all of this brand's templates.
          </p>
        </div>
        {canAdmin && (
          <Button variant="primary" disabled={!dirty || errors || save.isPending || live.pending} onClick={() => save.mutate({ mjml, version: l.version })}>
            {save.isPending ? "Saving…" : "Save layout"}
          </Button>
        )}
      </div>

      {save.error instanceof ApiError && save.error.code === "stale" ? (
        <p role="alert" className="rounded-md bg-amber-50 px-3 py-2 text-sm text-amber-900">
          Another admin changed the layout while you were editing. Copy your changes and reload.
        </p>
      ) : (
        <ErrorNote error={save.error} />
      )}
      {save.data?.templates_rebuilt ? (
        <p role="status" className="rounded-md bg-green-50 px-3 py-2 text-sm text-green-800">
          Saved. Recompiled {save.data.templates_rebuilt} {save.data.templates_rebuilt === 1 ? "template" : "templates"}
          {save.data.templates_with_errors ? `; ${save.data.templates_with_errors} now have errors to fix.` : "."}
        </p>
      ) : null}

      <div className="grid gap-4 xl:grid-cols-2">
        <Card title="MJML" description={canAdmin ? "A full MJML document with {{content}} where templates go." : "Only brand admins can change the layout."}>
          <div className="border-t border-zinc-100 p-3">
            <MJMLEditor value={mjml} onChange={setMJML} readOnly={!canAdmin} rows={32} />
          </div>
          <MergeTags tags={saved.tags} layout />
        </Card>
        <div className="space-y-4">
          <Card title="Checks" description="Checked with sample content in place of {{content}}.">
            <div className="border-t border-zinc-100">
              <ErrorNote error={live.error} />
              <IssueList issues={issues} checking={live.pending} />
            </div>
          </Card>
          <Card title="Preview">
            <div className="border-t border-zinc-100">
              <EmailPreview html={fresh?.preview_html ?? saved.preview_html} />
            </div>
          </Card>
        </div>
      </div>
    </div>
  );
}
