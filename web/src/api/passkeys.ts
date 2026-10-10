import { startAuthentication, startRegistration } from "@simplewebauthn/browser";
import type {
  PublicKeyCredentialCreationOptionsJSON,
  PublicKeyCredentialRequestOptionsJSON,
} from "@simplewebauthn/browser";
import { ApiError, apiFetch } from "./client";
import type { Me } from "./types";

// The Go server answers begin calls with { ceremony, options: { publicKey } }
// and expects the browser's credential JSON on /finish?ceremony=…
type Begin<T> = { ceremony: string; options: { publicKey: T } };

async function ceremony<T>(beginPath: string, finishPath: string, run: (opts: T) => Promise<unknown>): Promise<Me> {
  const begin = await apiFetch<Begin<T>>(beginPath, { method: "POST" });
  let credential: unknown;
  try {
    credential = await run(begin.options.publicKey);
  } catch (e) {
    // The user closed the browser's passkey dialog, or the device refused.
    const message = e instanceof Error ? e.message : String(e);
    throw new ApiError(0, "passkey_cancelled", message);
  }
  return apiFetch<Me>(`${finishPath}?ceremony=${encodeURIComponent(begin.ceremony)}`, {
    method: "POST",
    body: credential,
  });
}

const get = (optionsJSON: PublicKeyCredentialRequestOptionsJSON) => startAuthentication({ optionsJSON });
const create = (optionsJSON: PublicKeyCredentialCreationOptionsJSON) => startRegistration({ optionsJSON });

export const signInWithPasskey = () => ceremony("/admin/v1/auth/login/begin", "/admin/v1/auth/login/finish", get);

export const confirmWithPasskey = () => ceremony("/admin/v1/auth/stepup/begin", "/admin/v1/auth/stepup/finish", get);

export const acceptInviteWithPasskey = (token: string) =>
  ceremony(`/admin/v1/invites/${token}/begin`, `/admin/v1/invites/${token}/finish`, create);

// withStepUp runs a sensitive action; if the server asks for a fresh passkey
// confirmation, it shows the passkey prompt and retries once.
export async function withStepUp<T>(action: () => Promise<T>): Promise<T> {
  try {
    return await action();
  } catch (e) {
    if (e instanceof ApiError && e.code === "step_up_required") {
      await confirmWithPasskey();
      return action();
    }
    throw e;
  }
}
