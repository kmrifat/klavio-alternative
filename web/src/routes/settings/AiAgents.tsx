import { useState, type FormEvent } from "react";
import { Bot, Info, ShieldCheck, TriangleAlert } from "lucide-react";
import {
  mcpURL,
  useAgentTokens,
  useCreateAgentToken,
  useEveryonesAgentTokens,
  useRevokeAgentToken,
  type AgentToken,
  type SomeonesAgentToken,
} from "../../api/agents";
import { useMe } from "../../api/hooks";
import { Badge, Button, Card, CopyButton, ErrorNote, Field, Spinner, inputClass } from "../../components/ui";
import { shortDate, timeAgo } from "../../lib/format";

const stateBadge: Record<AgentToken["state"], { label: string; tone: "green" | "zinc" | "red" }> = {
  active: { label: "Active", tone: "green" },
  expired: { label: "Expired", tone: "zinc" },
  revoked: { label: "Revoked", tone: "red" },
};

// Settings → AI agents: tokens that let your AI agent (Claude Code, Codex,
// OpenCode…) use the console through MCP, as you.
export function AiAgents() {
  const tokens = useAgentTokens();
  const [created, setCreated] = useState<{ token: AgentToken; secret: string } | null>(null);

  return (
    <div className="space-y-4">
      <p className="flex gap-2 rounded-lg border border-sky-200 bg-sky-50 px-4 py-3 text-sm text-sky-900">
        <Info className="mt-0.5 size-4 shrink-0" />
        <span>
          <strong>Let your AI agent work in the console as you.</strong> It can read reports, health, campaigns, templates, segments and
          contacts, and prepare drafts. It can't send anything: it asks, and a person approves with their passkey. Everything it does is
          in the audit log, marked as done by your agent.
        </span>
      </p>
      <ClaudeCard />
      {created ? <NewToken created={created} onDone={() => setCreated(null)} /> : <CreateCard onCreated={setCreated} />}
      <Card title="Your agent tokens" description="Each lasts at most 90 days. Revoke one the moment you stop using it.">
        {tokens.isPending ? (
          <div className="border-t border-zinc-200 px-5 py-4">
            <Spinner />
          </div>
        ) : tokens.isError ? (
          <div className="border-t border-zinc-200 px-5 py-4">
            <ErrorNote error={tokens.error} />
          </div>
        ) : tokens.data.length === 0 ? (
          <p className="border-t border-zinc-200 px-5 py-4 text-sm text-zinc-600">No tokens yet.</p>
        ) : (
          <TokenTable tokens={tokens.data} />
        )}
      </Card>
      <EveryonesTokens />
      <Card title="What an agent can and can't do" description="The same rules as you, minus anything that needs your passkey">
        <div className="grid gap-4 border-t border-zinc-200 px-5 py-4 text-sm sm:grid-cols-2">
          <div>
            <p className="flex items-center gap-1.5 font-medium text-green-800">
              <ShieldCheck className="size-4" /> Can, within your role
            </p>
            <ul className="mt-1.5 list-disc space-y-0.5 pl-5 text-zinc-700">
              <li>Read brands, health, reports and approvals</li>
              <li>Search and read contacts</li>
              <li>Draft campaigns, templates and segments</li>
              <li>Add a contact who agreed, with proof</li>
              <li>Ask for a send (you approve it)</li>
            </ul>
          </div>
          <div>
            <p className="flex items-center gap-1.5 font-medium text-red-800">
              <TriangleAlert className="size-4" /> Can't
            </p>
            <ul className="mt-1.5 list-disc space-y-0.5 pl-5 text-zinc-700">
              <li>Send, schedule, pause or cancel email</li>
              <li>Approve or reject anything</li>
              <li>Delete, unsubscribe, block or erase people</li>
              <li>Change settings, limits, team or API keys</li>
              <li>Export personal data</li>
            </ul>
          </div>
        </div>
      </Card>
    </div>
  );
}

// Claude (claude.ai and Desktop) signs in by itself: no token to copy.
function ClaudeCard() {
  const url = mcpURL();
  return (
    <Card title="Claude (claude.ai or Desktop)" description="No token needed: Claude signs in, and you approve with your passkey">
      <ol className="list-decimal space-y-1 border-t border-zinc-200 py-4 pr-5 pl-10 text-sm text-zinc-700">
        <li>In Claude, open Settings → Connectors and add a custom connector.</li>
        <li>
          Paste this address: <code className="rounded bg-zinc-100 px-1 font-mono text-xs">{url}</code> <CopyButton text={url} label="Copy address" />
        </li>
        <li>Claude sends you here to approve. Its connection then shows below, where you can revoke it.</li>
      </ol>
      <p className="px-5 pb-4 text-xs text-zinc-500">Claude connects from the internet, so this works once the console is online, not on your own computer.</p>
    </Card>
  );
}

function CreateCard({ onCreated }: { onCreated: (c: { token: AgentToken; secret: string }) => void }) {
  const create = useCreateAgentToken();
  const [name, setName] = useState("");
  const [days, setDays] = useState(30);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    create.mutate({ name: name.trim(), expires_in_days: days }, { onSuccess: (c) => (setName(""), onCreated(c)) });
  };
  return (
    <Card title="Claude Code, Codex, OpenCode and others" description="Make a token for one agent on one computer, so you can revoke it on its own">
      <form onSubmit={submit} className="space-y-3 border-t border-zinc-200 px-5 py-4">
        <Field label="Name">
          {(id) => (
            <input id={id} className={inputClass} value={name} maxLength={60} onChange={(e) => setName(e.target.value)} placeholder="Claude Code on my laptop" />
          )}
        </Field>
        <div className="flex flex-wrap items-end justify-between gap-3">
          <Field label="Expires after">
            {(id) => (
              <select id={id} className={inputClass} value={days} onChange={(e) => setDays(Number(e.target.value))}>
                <option value={7}>7 days</option>
                <option value={30}>30 days</option>
                <option value={90}>90 days</option>
              </select>
            )}
          </Field>
          <Button type="submit" variant="primary" disabled={!name.trim() || create.isPending}>
            <Bot className="size-4" />
            {create.isPending ? "Creating…" : "Create token"}
          </Button>
        </div>
      </form>
      {create.error ? (
        <div className="px-5 pb-4">
          <ErrorNote error={create.error} />
        </div>
      ) : null}
    </Card>
  );
}

// The setup for each agent, with the token filled in.
function setups(url: string, secret: string) {
  return [
    { agent: "Claude Code", where: "Run in a terminal", code: `claude mcp add --transport http mail-console ${url} --header "Authorization: Bearer ${secret}"` },
    {
      agent: "Codex",
      where: "Add to ~/.codex/config.toml, and put the token in MAIL_CONSOLE_TOKEN in your shell",
      code: `[mcp_servers.mail_console]\nurl = "${url}"\nbearer_token_env_var = "MAIL_CONSOLE_TOKEN"\n\n# in your shell profile:\n# export MAIL_CONSOLE_TOKEN="${secret}"`,
    },
    {
      agent: "OpenCode",
      where: "Add to opencode.json",
      code: JSON.stringify({ mcp: { "mail-console": { type: "remote", url, headers: { Authorization: `Bearer ${secret}` } } } }, null, 2),
    },
    { agent: "Anything else", where: "Any MCP client that supports HTTP servers with a header", code: `URL:    ${url}\nHeader: Authorization: Bearer ${secret}` },
  ];
}

function NewToken({ created, onDone }: { created: { token: AgentToken; secret: string }; onDone: () => void }) {
  const url = mcpURL();
  const [agent, setAgent] = useState(0);
  const all = setups(url, created.secret);
  const s = all[agent]!;
  return (
    <Card title={`"${created.token.name}" is ready`} description="Copy it now: the token is shown only once">
      <div className="space-y-4 border-t border-zinc-200 px-5 py-4 text-sm">
        <div>
          <p className="font-medium">Token</p>
          <div className="mt-1 flex items-center gap-2 rounded-md border border-amber-200 bg-amber-50 px-3 py-2">
            <code className="min-w-0 flex-1 font-mono text-xs [overflow-wrap:anywhere]">{created.secret}</code>
            <CopyButton text={created.secret} label="Copy token" />
          </div>
          <p className="mt-1 text-xs text-zinc-500">Treat it like a password: anyone with it can act as you, within the limits above.</p>
        </div>
        <div>
          <div role="tablist" aria-label="Agent" className="flex flex-wrap gap-1">
            {all.map((x, i) => (
              <button
                key={x.agent}
                role="tab"
                aria-selected={i === agent}
                onClick={() => setAgent(i)}
                className={`rounded-md px-2.5 py-1 text-xs font-medium ${i === agent ? "bg-indigo-50 text-indigo-700" : "text-zinc-600 hover:bg-zinc-100"}`}
              >
                {x.agent}
              </button>
            ))}
          </div>
          <p className="mt-2 text-xs text-zinc-600">{s.where}:</p>
          <div className="mt-1 flex items-start gap-2 rounded-md border border-zinc-200 bg-zinc-50 px-3 py-2">
            <pre className="min-w-0 flex-1 font-mono text-xs whitespace-pre-wrap [overflow-wrap:anywhere]">{s.code}</pre>
            <CopyButton text={s.code} label={`Copy ${s.agent} setup`} />
          </div>
        </div>
        <div className="flex justify-end">
          <Button onClick={onDone}>I've copied it</Button>
        </div>
      </div>
    </Card>
  );
}

// Owners: every agent that can still get in, whoever's it is. Someone who
// leaves keeps their agents until these are revoked: Cloudflare Access
// doesn't stand in front of the agents' address.
function EveryonesTokens() {
  const isOwner = useMe().data?.user.company_role === "owner";
  const tokens = useEveryonesAgentTokens(isOwner);
  const revoke = useRevokeAgentToken();
  if (!isOwner) return null;
  return (
    <Card
      title="Everyone's agents"
      description="Every agent token that still works. When someone leaves, revoke theirs here: removing them from Cloudflare Access doesn't stop their agents."
    >
      {tokens.isPending ? (
        <div className="border-t border-zinc-200 px-5 py-4">
          <Spinner />
        </div>
      ) : tokens.isError ? (
        <div className="border-t border-zinc-200 px-5 py-4">
          <ErrorNote error={tokens.error} />
        </div>
      ) : tokens.data.length === 0 ? (
        <p className="border-t border-zinc-200 px-5 py-4 text-sm text-zinc-600">No working agent tokens.</p>
      ) : (
        <ul className="divide-y divide-zinc-100 border-t border-zinc-200">
          {tokens.data.map((t: SomeonesAgentToken) => (
            <li key={t.id} className="flex flex-wrap items-center justify-between gap-2 px-5 py-2.5 text-sm">
              <div className="min-w-0">
                <p className="font-medium">
                  {t.name} <span className="font-mono text-xs font-normal text-zinc-500">{t.prefix}…</span>
                </p>
                <p className="text-xs text-zinc-600 [overflow-wrap:anywhere]">
                  {t.user_name} · {t.user_email} · last used {t.last_used_at ? timeAgo(t.last_used_at) : "never"}
                </p>
              </div>
              <Button size="sm" variant="ghost" disabled={revoke.isPending} onClick={() => revoke.mutate(t.id)} aria-label={`Revoke ${t.name} of ${t.user_email}`}>
                Revoke
              </Button>
            </li>
          ))}
        </ul>
      )}
      {revoke.error ? (
        <div className="px-5 py-3">
          <ErrorNote error={revoke.error} />
        </div>
      ) : null}
    </Card>
  );
}

function TokenTable({ tokens }: { tokens: AgentToken[] }) {
  const revoke = useRevokeAgentToken();
  return (
    <>
      <table className="w-full text-sm">
        <thead>
          <tr className="border-y border-zinc-200 bg-zinc-50 text-left text-[11px] tracking-wide text-zinc-500 uppercase">
            <th className="px-5 py-2 font-medium">Agent</th>
            <th className="px-3 py-2 font-medium">Last used</th>
            <th className="px-3 py-2 font-medium">Expires</th>
            <th className="px-5 py-2 text-right font-medium">State</th>
          </tr>
        </thead>
        <tbody>
          {tokens.map((t) => (
            <tr key={t.id} className="border-b border-zinc-100 last:border-0">
              <td className="px-5 py-2.5">
                <p className="font-medium">{t.name}</p>
                <p className="font-mono text-xs text-zinc-500">{t.prefix}…</p>
                {t.oauth_client_id && <p className="text-xs text-violet-700">Connected by signing in</p>}
              </td>
              <td className="px-3 py-2.5 text-zinc-600">{t.last_used_at ? timeAgo(t.last_used_at) : "Never"}</td>
              <td className="px-3 py-2.5 text-zinc-600">{shortDate(t.expires_at)}</td>
              <td className="px-5 py-2.5 text-right">
                <span className="inline-flex items-center gap-2">
                  <Badge tone={stateBadge[t.state].tone} dot>
                    {stateBadge[t.state].label}
                  </Badge>
                  {t.state === "active" && (
                    <Button size="sm" variant="ghost" disabled={revoke.isPending} onClick={() => revoke.mutate(t.id)} aria-label={`Revoke ${t.name}`}>
                      Revoke
                    </Button>
                  )}
                </span>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {revoke.error ? (
        <div className="px-5 py-3">
          <ErrorNote error={revoke.error} />
        </div>
      ) : null}
    </>
  );
}
