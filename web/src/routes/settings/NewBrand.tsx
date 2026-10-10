import { useState } from "react";
import { Navigate, useNavigate } from "react-router";
import { useCreateBrand, useMe, type NewBrand as NewBrandInput } from "../../api/hooks";
import { Button, Card, ErrorNote, Field, inputClass } from "../../components/ui";
import { slugify } from "../../lib/format";

export function NewBrand() {
  const isOwner = useMe().data?.user.company_role === "owner";
  const create = useCreateBrand();
  const navigate = useNavigate();
  const [form, setForm] = useState<NewBrandInput>({
    name: "",
    slug: "",
    color: "#5b5bd6",
    postal_address: "",
    sending_domain: "",
    link_domain: "",
  });
  const [slugTouched, setSlugTouched] = useState(false);

  if (!isOwner) return <Navigate to="/settings/brands" replace />;

  const set = (k: keyof NewBrandInput) => (e: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) =>
    setForm((f) => {
      const next = { ...f, [k]: e.target.value };
      if (k === "name" && !slugTouched) next.slug = slugify(e.target.value);
      return next;
    });

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    create.mutate(form, { onSuccess: (d) => navigate(`/settings/brands/${d.brand.slug}`) });
  };

  return (
    <Card title="Add brand" description="We'll set the brand up in Amazon SES and list the DNS records to publish. Nothing can be sent until they're in place.">
      <form onSubmit={submit} className="grid gap-4 border-t border-zinc-100 px-5 py-5 sm:grid-cols-2">
        <Field label="Brand name">{(id) => <input id={id} required className={inputClass} value={form.name} onChange={set("name")} />}</Field>
        <Field label="Short name (URL)" hint="Lowercase letters, digits and dashes. Can't be changed later.">
          {(id) => (
            <input
              id={id}
              required
              className={`${inputClass} font-mono`}
              value={form.slug}
              onChange={(e) => {
                setSlugTouched(true);
                set("slug")(e);
              }}
            />
          )}
        </Field>
        <Field label="Sending domain" hint="A subdomain only for marketing email, e.g. news.yourbrand.com. Can't be changed later.">
          {(id) => (
            <input id={id} required placeholder="news.yourbrand.com" className={`${inputClass} font-mono`} value={form.sending_domain} onChange={set("sending_domain")} />
          )}
        </Field>
        <Field label="Link domain" hint="Used for tracked links and unsubscribe pages, e.g. go.yourbrand.com.">
          {(id) => (
            <input id={id} required placeholder="go.yourbrand.com" className={`${inputClass} font-mono`} value={form.link_domain} onChange={set("link_domain")} />
          )}
        </Field>
        <Field label="Brand color">
          {(id) => (
            <div className="flex items-center gap-2">
              <input id={id} type="color" className="h-9 w-12 rounded border border-zinc-200" value={form.color} onChange={set("color")} />
              <code className="text-sm text-zinc-600">{form.color}</code>
            </div>
          )}
        </Field>
        <Field label="Postal address" hint="Required by law in every marketing email footer.">
          {(id) => <textarea id={id} required rows={2} className={inputClass} value={form.postal_address} onChange={set("postal_address")} />}
        </Field>
        <div className="sm:col-span-2">
          <ErrorNote error={create.error} />
        </div>
        <div className="flex justify-end gap-2 sm:col-span-2">
          <Button onClick={() => navigate("/settings/brands")}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={create.isPending}>
            {create.isPending ? "Setting up…" : "Add brand"}
          </Button>
        </div>
      </form>
    </Card>
  );
}
