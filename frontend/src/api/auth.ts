const STORAGE_KEY = "pgqn_api_key";
const ORG_STORAGE_KEY = "pgqn_organization_id";

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

export function getPreferredOrgId(): string {
  try {
    return localStorage.getItem(ORG_STORAGE_KEY)?.trim() ?? "";
  } catch {
    return "";
  }
}

export function setPreferredOrgId(orgId: string): void {
  try {
    const trimmed = orgId.trim();
    if (trimmed) {
      localStorage.setItem(ORG_STORAGE_KEY, trimmed);
    } else {
      localStorage.removeItem(ORG_STORAGE_KEY);
    }
  } catch {
    // ignore storage errors
  }
}

export function authHeaders(): Record<string, string> {
  const headers: Record<string, string> = {};
  const key = getApiKey();
  if (key) headers.Authorization = `Bearer ${key}`;
  const org = getPreferredOrgId();
  if (org) headers["X-Organization-ID"] = org;
  return headers;
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

export interface MeResponse {
  user_id: string;
  organization_id: string;
  role: string;
}

export interface OrganizationMembership {
  organization_id: string;
  role: string;
  name: string;
  slug: string;
  user_id?: string;
}

export async function fetchMe(): Promise<MeResponse | null> {
  try {
    const res = await fetch("/api/v1/me", authFetchInit());
    if (!res.ok) return null;
    return res.json();
  } catch {
    return null;
  }
}

export async function fetchMyOrganizations(): Promise<OrganizationMembership[]> {
  try {
    const res = await fetch("/api/v1/me/organizations", authFetchInit());
    if (!res.ok) return [];
    const body = (await res.json()) as { organizations?: OrganizationMembership[] };
    return body.organizations ?? [];
  } catch {
    return [];
  }
}

export async function switchOrganization(organizationId: string): Promise<MeResponse | null> {
  try {
    const res = await fetch(
      "/api/v1/me/organization",
      authFetchInit({
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ organization_id: organizationId }),
      })
    );
    if (!res.ok) return null;
    const me = (await res.json()) as MeResponse;
    setPreferredOrgId(me.organization_id);
    return me;
  } catch {
    return null;
  }
}
