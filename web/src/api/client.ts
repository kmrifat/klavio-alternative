// Empty = same origin (Vite proxies /admin to the Go API in development).
export const API_URL: string = import.meta.env.VITE_API_URL ?? "";

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message);
  }
}

// apiFetch sends the session cookie, encodes JSON bodies and throws ApiError
// (with the server's error code) on non-2xx responses.
export async function apiFetch<T>(path: string, init?: Omit<RequestInit, "body"> & { body?: unknown }): Promise<T> {
  const { body, ...rest } = init ?? {};
  const method = rest.method ?? (body === undefined ? "GET" : "POST");
  const res = await fetch(`${API_URL}${path}`, {
    credentials: "include",
    ...rest,
    method,
    headers: {
      Accept: "application/json",
      ...(method !== "GET" ? { "Content-Type": "application/json" } : {}),
      ...rest.headers,
    },
    body: method !== "GET" ? JSON.stringify(body ?? {}) : undefined,
  });
  if (!res.ok) {
    let code = "http_error";
    let message = `${method} ${path} failed: ${res.status}`;
    try {
      const data = (await res.json()) as { error?: { code: string; message: string } };
      if (data.error) ({ code, message } = data.error);
    } catch {
      /* not JSON */
    }
    throw new ApiError(res.status, code, message);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}
