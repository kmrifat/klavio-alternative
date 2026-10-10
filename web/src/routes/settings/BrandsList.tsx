import { Link } from "react-router";
import { ChevronRight, Plus } from "lucide-react";
import { useBrands, useMe } from "../../api/hooks";
import { Badge, Button, Card, ErrorNote, Spinner } from "../../components/ui";
import { BrandMark } from "../../layout/BrandSwitcher";
import { brandStatus, timeAgo } from "../../lib/format";

export function BrandsList() {
  const brands = useBrands();
  const isOwner = useMe().data?.user.company_role === "owner";

  return (
    <Card
      title="Brands & domains"
      description="Each brand sends from its own subdomain, so one brand's reputation can't hurt another's — or your team's everyday email."
      action={
        isOwner && (
          <Link to="new">
            <Button variant="primary" size="sm">
              <Plus className="size-3.5" /> Add brand
            </Button>
          </Link>
        )
      }
    >
      {brands.isPending ? (
        <div className="px-5">
          <Spinner />
        </div>
      ) : brands.isError ? (
        <div className="px-5 pb-5">
          <ErrorNote error={brands.error} />
        </div>
      ) : brands.data.length === 0 ? (
        <p className="border-t border-zinc-100 px-5 py-8 text-center text-sm text-zinc-500">
          No brands yet.{isOwner && " Add one to set up its sending domain."}
        </p>
      ) : (
        <ul>
          {brands.data.map((b) => {
            const st = brandStatus[b.status];
            return (
              <li key={b.id} className="border-t border-zinc-100">
                <Link to={b.slug} className="flex items-center gap-3 px-5 py-3 hover:bg-zinc-50">
                  <BrandMark brand={b} />
                  <div className="min-w-0 flex-1">
                    <p className="text-sm font-medium">{b.name}</p>
                    <p className="truncate font-mono text-xs text-zinc-500">
                      {b.sending_domain} · links {b.link_domain}
                    </p>
                  </div>
                  <Badge tone={st.tone} dot>
                    {st.label}
                  </Badge>
                  <span className="w-28 text-right text-xs text-zinc-500">checked {timeAgo(b.dns_checked_at)}</span>
                  <ChevronRight className="size-4 text-zinc-400" />
                </Link>
              </li>
            );
          })}
        </ul>
      )}
    </Card>
  );
}
