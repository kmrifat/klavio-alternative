import { useEffect, useRef, useState } from "react";
import { Link, useNavigate } from "react-router";
import { Check, ChevronDown, Plus } from "lucide-react";
import type { BrandListItem } from "../api/types";
import { useMe } from "../api/hooks";

// "Sending as" card from the design. No brand selected = read-only company view.
export function BrandSwitcher({ brands, current }: { brands: BrandListItem[]; current: BrandListItem | undefined }) {
  const [open, setOpen] = useState(false);
  const navigate = useNavigate();
  const ref = useRef<HTMLDivElement>(null);
  const isOwner = useMe().data?.user.company_role === "owner";

  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) setOpen(false);
    };
    const esc = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", esc);
    return () => {
      document.removeEventListener("mousedown", close);
      document.removeEventListener("keydown", esc);
    };
  }, [open]);

  if (brands.length === 0) {
    return (
      <div className="rounded-lg border border-dashed border-zinc-300 bg-white px-3 py-2.5 text-sm">
        <p className="font-medium">No brands yet</p>
        {isOwner ? (
          <Link to="/settings/brands/new" className="mt-1 inline-flex items-center gap-1 text-xs font-medium text-accent">
            <Plus className="size-3" /> Add your first brand
          </Link>
        ) : (
          <p className="mt-0.5 text-xs text-zinc-500">An owner needs to give you access.</p>
        )}
      </div>
    );
  }

  const go = (to: string) => {
    setOpen(false);
    navigate(to);
  };

  return (
    <div ref={ref} className="relative">
      <button
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className="flex w-full items-center gap-2.5 rounded-lg border border-zinc-200 bg-white px-2.5 py-2 text-left shadow-xs hover:border-zinc-300"
      >
        {current ? (
          <>
            <BrandMark brand={current} />
            <span className="min-w-0 flex-1">
              <span className="block text-[10px] font-medium tracking-wide text-zinc-500 uppercase">Sending as</span>
              <span className="block truncate text-sm font-medium">{current.name}</span>
              <span className="block truncate font-mono text-[11px] text-zinc-500">{current.sending_domain}</span>
            </span>
          </>
        ) : (
          <>
            <span className="grid size-7 shrink-0 place-items-center rounded-md bg-zinc-900 text-xs font-semibold text-white">
              {brands.length}
            </span>
            <span className="min-w-0 flex-1">
              <span className="block text-[10px] font-medium tracking-wide text-zinc-500 uppercase">Company view</span>
              <span className="block text-sm font-medium">All brands</span>
              <span className="block font-mono text-[11px] text-zinc-500">View only</span>
            </span>
          </>
        )}
        <ChevronDown className="size-4 shrink-0 text-zinc-400" />
      </button>

      {open && (
        <div role="menu" className="absolute inset-x-0 top-full z-10 mt-1 rounded-lg border border-zinc-200 bg-white p-1 shadow-lg">
          <MenuItem selected={!current} onSelect={() => go("/safety")}>
            <span className="text-sm">All brands (company view)</span>
          </MenuItem>
          <div className="my-1 border-t border-zinc-100" />
          {brands.map((b) => (
            <MenuItem key={b.slug} selected={current?.slug === b.slug} onSelect={() => go(`/b/${b.slug}/campaigns`)}>
              <BrandMark brand={b} small />
              <span className="text-sm">{b.name}</span>
            </MenuItem>
          ))}
        </div>
      )}
    </div>
  );
}

function MenuItem({ selected, onSelect, children }: { selected: boolean; onSelect: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      role="menuitemradio"
      aria-checked={selected}
      onClick={onSelect}
      className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left hover:bg-zinc-100"
    >
      {children}
      {selected && <Check className="ml-auto size-4 text-accent" />}
    </button>
  );
}

export function BrandMark({ brand, small }: { brand: Pick<BrandListItem, "name" | "color">; small?: boolean }) {
  return (
    <span
      className={`grid shrink-0 place-items-center rounded-md font-semibold text-white ${small ? "size-5 text-[10px]" : "size-7 text-xs"}`}
      style={{ background: brand.color }}
    >
      {brand.name[0]}
    </span>
  );
}
