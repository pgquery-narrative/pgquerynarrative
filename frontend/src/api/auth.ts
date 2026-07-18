const STORAGE_KEY = "pgqn_api_key";

/**
 * Browser localStorage is readable by any script on the page (XSS, malicious
 * extensions, etc.), so storing a long-lived API key there is unsafe in a
 * production deployment. It remains available for local development and for
 * the CLI (which does not use this browser storage path at all — see
 * cmd/ tooling) and can be explicitly opted into for a given deployment via
 * VITE_ALLOW_BROWSER_API_KEY=true when the operator has accepted the risk
 * (e.g. an internal-only tool).
 */
export function isBrowserKeyStorageAllowed(): boolean {
  try {
    const env = import.meta.env;
    return Boolean(env?.DEV) || env?.VITE_ALLOW_BROWSER_API_KEY === "true";
  } catch {
    return false;
  }
}

export function getApiKey(): string {
  if (!isBrowserKeyStorageAllowed()) return "";
  try {
    return localStorage.getItem(STORAGE_KEY)?.trim() ?? "";
  } catch {
    return "";
  }
}

export function setApiKey(key: string): void {
  if (!isBrowserKeyStorageAllowed()) return;
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

/** Removes any previously stored browser API key, regardless of the current flag state. */
export function clearApiKey(): void {
  try {
    localStorage.removeItem(STORAGE_KEY);
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

export async function fetchSessionStatus(): Promise<{
  authenticated: boolean;
  user_id?: string;
  role?: string;
  org_id?: string;
}> {
  try {
    const res = await fetch("/auth/session", { credentials: "include" });
    if (!res.ok) return { authenticated: false };
    return res.json();
  } catch {
    return { authenticated: false };
  }
}
