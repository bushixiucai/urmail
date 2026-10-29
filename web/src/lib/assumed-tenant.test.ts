import { describe, it, expect, beforeEach } from "vitest";
import {
  getAssumedTenant,
  setAssumedTenant,
  clearAssumedTenant,
} from "./assumed-tenant";

describe("assumed-tenant helpers", () => {
  beforeEach(() => {
    sessionStorage.clear();
  });

  it("returns null when no assumed tenant in storage", () => {
    expect(getAssumedTenant()).toBeNull();
  });

  it("stores and retrieves assumed tenant", () => {
    setAssumedTenant({ id: 42, name: "Acme Corp", slug: "acme" });
    expect(getAssumedTenant()).toEqual({
      id: 42,
      name: "Acme Corp",
      slug: "acme",
    });
  });

  it("clears assumed tenant", () => {
    setAssumedTenant({ id: 42, name: "Acme Corp", slug: "acme" });
    clearAssumedTenant();
    expect(getAssumedTenant()).toBeNull();
  });
});
