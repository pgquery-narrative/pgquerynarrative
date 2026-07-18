import { afterEach, describe, expect, it, vi } from "vitest";
import { authHeaders, getApiKey, isBrowserKeyStorageAllowed, setApiKey } from "./auth";

describe("auth helpers", () => {
  afterEach(() => {
    localStorage.clear();
    vi.unstubAllEnvs();
    vi.restoreAllMocks();
  });

  it("stores and reads API key in dev mode", () => {
    setApiKey("  secret-key  ");
    expect(getApiKey()).toBe("secret-key");
  });

  it("clears API key when empty", () => {
    setApiKey("abc");
    setApiKey("");
    expect(getApiKey()).toBe("");
  });

  it("builds Authorization header when key present", () => {
    setApiKey("token-123");
    expect(authHeaders()).toEqual({ Authorization: "Bearer token-123" });
  });

  it("returns empty headers without key", () => {
    expect(authHeaders()).toEqual({});
  });

  it("does not persist API key when browser storage is disallowed", () => {
    vi.stubEnv("DEV", false);
    expect(isBrowserKeyStorageAllowed()).toBe(false);
    setApiKey("secret-key");
    expect(getApiKey()).toBe("");
    expect(localStorage.getItem("pgqn_api_key")).toBeNull();
  });

  it("allows browser storage when VITE_ALLOW_BROWSER_API_KEY is set", () => {
    vi.stubEnv("DEV", false);
    vi.stubEnv("VITE_ALLOW_BROWSER_API_KEY", "true");
    expect(isBrowserKeyStorageAllowed()).toBe(true);
    setApiKey("secret-key");
    expect(getApiKey()).toBe("secret-key");
  });
});
