import { afterEach, describe, expect, it, vi } from "vitest";
import { authHeaders, getApiKey, setApiKey } from "./auth";

describe("auth helpers", () => {
  afterEach(() => {
    localStorage.clear();
    vi.restoreAllMocks();
  });

  it("stores and reads API key", () => {
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
});
