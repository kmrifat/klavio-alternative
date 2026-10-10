import { useMeta } from "../api/client";

// Shows whether the Go API is reachable. Replaced by the brand health pill in M6.
export function ApiStatus() {
  const { data, isPending, isError } = useMeta();

  const [dot, label, tone] = isPending
    ? ["bg-zinc-400", "Connecting…", "bg-zinc-100 text-zinc-600"]
    : isError
      ? ["bg-red-500", "API offline", "bg-red-50 text-red-700"]
      : ["bg-green-500", `API ${data.version}`, "bg-green-50 text-green-700"];

  return (
    <span role="status" className={`flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-medium ${tone}`}>
      <span className={`size-1.5 rounded-full ${dot}`} />
      {label}
    </span>
  );
}
