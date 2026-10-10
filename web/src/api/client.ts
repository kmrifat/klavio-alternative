import { useQuery } from "@tanstack/react-query";

export const API_URL: string = import.meta.env.VITE_API_URL ?? "http://localhost:8080";

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

// apiFetch sends the session cookie and throws ApiError on non-2xx responses.
export async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${API_URL}${path}`, {
    credentials: "include",
    ...init,
    headers: { Accept: "application/json", ...init?.headers },
  });
  if (!res.ok) {
    throw new ApiError(res.status, `${init?.method ?? "GET"} ${path} failed: ${res.status}`);
  }
  return (await res.json()) as T;
}

export type Meta = { version: string; env: string };

export function useMeta() {
  return useQuery({
    queryKey: ["meta"],
    queryFn: () => apiFetch<Meta>("/admin/v1/meta"),
    refetchInterval: 30_000,
  });
}
