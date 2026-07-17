const STORAGE_KEY = "pgqn_api_key";

export function getApiKey(): string {
  try {
    return localStorage.getItem(STORAGE_KEY)?.trim() ?? "";
  } catch {
    return "";
  }
}

export function setApiKey(key: string): void {
  try {
    const trimmed = key.trim();
    if (trimmed) {
      localStorage.setItem(STORAGE_KEY, trimmed);
    } else {
      localStorage.removeItem(STORAGE_KEY);
    }
  } catch {
    // ignore storage errors
  }
}

export function authHeaders(): Record<string, string> {
  const key = getApiKey();
  if (!key) return {};
  return { Authorization: `Bearer ${key}` };
}

/** Fetch init that sends session cookies and optional API key. */
export function authFetchInit(init?: RequestInit): RequestInit {
  return {
    credentials: "include",
    ...init,
    headers: { ...authHeaders(), ...(init?.headers as Record<string, string> | undefined) },
  };
}

export async function fetchSessionStatus(): Promise<{ authenticated: boolean; user_id?: string }> {
  try {
    const res = await fetch("/auth/session", { credentials: "include" });
    if (!res.ok) return { authenticated: false };
    return res.json();
  } catch {
    return { authenticated: false };
  }
}
