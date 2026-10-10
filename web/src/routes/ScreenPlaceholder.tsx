import { useParams } from "react-router";
import { Construction } from "lucide-react";
import type { Screen } from "../lib/screens";
import { useBrands } from "../api/hooks";

// Stand-in for a screen until its milestone builds it.
export function ScreenPlaceholder({ screen }: { screen: Screen }) {
  const slug = useParams().brand;
  const brand = useBrands().data?.find((b) => b.slug === slug);

  return (
    <div className="max-w-5xl">
      <h1 className="text-2xl font-semibold tracking-tight">{screen.title}</h1>
      <p className="mt-1 text-sm text-zinc-600">
        {screen.description}
        {brand && <> Brand: {brand.name}.</>}
      </p>

      <div className="mt-8 flex items-start gap-3 rounded-xl border border-dashed border-zinc-300 bg-zinc-50 p-5">
        <Construction className="mt-0.5 size-5 text-zinc-500" />
        <div>
          <p className="text-sm font-medium">
            Design frame {screen.id} · built in milestone {screen.milestone}
          </p>
          <p className="mt-0.5 text-sm text-zinc-600">See PLAN.md §7 for what this screen needs.</p>
        </div>
      </div>
    </div>
  );
}
