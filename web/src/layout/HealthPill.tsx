import { Link } from "react-router";
import { levelInfo, useBrandHealth } from "../api/health";

// The brand's health score in the top bar, linking to Brand health (S2).
export function HealthPill({ slug }: { slug: string }) {
  const { data } = useBrandHealth(slug);
  if (!data) return null;
  const lv = levelInfo[data.health.level];
  const dot = { green: "bg-green-500", amber: "bg-amber-500", red: "bg-red-500", zinc: "bg-zinc-400" }[lv.tone];
  const tone = { green: "bg-green-50 text-green-700", amber: "bg-amber-50 text-amber-700", red: "bg-red-50 text-red-700", zinc: "bg-zinc-100 text-zinc-600" }[lv.tone];
  return (
    <Link
      to={`/b/${slug}/health`}
      aria-label={`Brand health: ${data.health.score ?? "no data"}`}
      className={`flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-medium ${tone}`}
    >
      <span className={`size-1.5 rounded-full ${dot}`} />
      Health {data.health.score ?? "—"} · {lv.label}
    </Link>
  );
}
