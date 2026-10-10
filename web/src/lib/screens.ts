import type { LucideIcon } from "lucide-react";
import {
  BookTemplate,
  Contact,
  FileClock,
  Filter,
  Gauge,
  KeyRound,
  Send,
  Server,
  Settings,
  ShieldCheck,
  UserCheck,
} from "lucide-react";

// Every screen in design.fig, the milestone that builds it, and where it lives.
// Brand screens are under /b/:brand/…, company screens at the root.
export type Screen = {
  id: string; // design frame, e.g. "S3"
  title: string;
  path: string; // relative path segment
  scope: "brand" | "company";
  milestone: string;
  description: string;
  icon: LucideIcon;
};

export const screens: Screen[] = [
  {
    id: "S10",
    title: "Campaigns",
    path: "campaigns",
    scope: "brand",
    milestone: "M3",
    description: "Draft, review, approve and send campaigns for this brand.",
    icon: Send,
  },
  {
    id: "S3",
    title: "Contacts",
    path: "contacts",
    scope: "brand",
    milestone: "M2",
    description: "Subscribers with engagement tier and consent proof.",
    icon: Contact,
  },
  {
    id: "S12",
    title: "Segments",
    path: "segments",
    scope: "brand",
    milestone: "M7",
    description: "Saved, always-up-to-date groups. Unsafe people are removed at send time.",
    icon: Filter,
  },
  {
    id: "S13",
    title: "Templates",
    path: "templates",
    scope: "brand",
    milestone: "M3",
    description: "Templates inside the locked brand layout.",
    icon: BookTemplate,
  },
  {
    id: "S14",
    title: "Contacts API",
    path: "api",
    scope: "brand",
    milestone: "M7",
    description: "API keys, consent wordings and double opt-in for your sites and apps.",
    icon: Server,
  },
  {
    id: "S2",
    title: "Brand health",
    path: "health",
    scope: "brand",
    milestone: "M6",
    description: "Authentication, reputation, warm-up and the automatic safety brake.",
    icon: ShieldCheck,
  },
  {
    id: "S1",
    title: "Safety center",
    path: "safety",
    scope: "company",
    milestone: "M6",
    description: "Health of every brand and the shared AWS account.",
    icon: Gauge,
  },
  {
    id: "S15",
    title: "Approvals",
    path: "approvals",
    scope: "company",
    milestone: "M5",
    description: "Requests waiting for a second person. Nobody approves their own.",
    icon: UserCheck,
  },
  {
    id: "S16",
    title: "Audit log",
    path: "audit",
    scope: "company",
    milestone: "M5",
    description: "Append-only record of who did what, where and when.",
    icon: FileClock,
  },
  {
    id: "S9",
    title: "Team & access",
    path: "team",
    scope: "company",
    milestone: "M1",
    description: "Members, roles per brand and security settings.",
    icon: KeyRound,
  },
  {
    id: "S17",
    title: "Settings",
    path: "settings",
    scope: "company",
    milestone: "M1",
    description: "Brands & domains, sending providers, safety limits, API keys, data & privacy.",
    icon: Settings,
  },
];

export const brandScreens = screens.filter((s) => s.scope === "brand");
export const companyScreens = screens.filter((s) => s.scope === "company" && s.path !== "settings");
export const settingsScreen = screens.find((s) => s.path === "settings")!;
