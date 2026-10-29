import { useState, useEffect, useCallback } from "react";

export interface AssumedTenant {
  id: number;
  name: string;
  slug: string;
}

const STORAGE_KEY = "urmail_assumed_tenant";
const EVENT_NAME = "urmail_assumed_tenant_change";

export function getAssumedTenant(): AssumedTenant | null {
  if (typeof window === "undefined" || !window.sessionStorage) {
    return null;
  }
  try {
    const raw = window.sessionStorage.getItem(STORAGE_KEY);
    if (!raw) return null;
    return JSON.parse(raw) as AssumedTenant;
  } catch {
    return null;
  }
}

export function setAssumedTenant(tenant: AssumedTenant): void {
  if (typeof window === "undefined" || !window.sessionStorage) {
    return;
  }
  try {
    window.sessionStorage.setItem(STORAGE_KEY, JSON.stringify(tenant));
    window.dispatchEvent(new CustomEvent(EVENT_NAME, { detail: tenant }));
  } catch {
    // ignore storage quota / sandbox errors
  }
}

export function clearAssumedTenant(): void {
  if (typeof window === "undefined" || !window.sessionStorage) {
    return;
  }
  try {
    window.sessionStorage.removeItem(STORAGE_KEY);
    window.dispatchEvent(new CustomEvent(EVENT_NAME, { detail: null }));
  } catch {
    // ignore
  }
}

export function useAssumedTenant() {
  const [assumedTenant, setAssumed] = useState<AssumedTenant | null>(() =>
    getAssumedTenant(),
  );

  useEffect(() => {
    const handler = (e: Event) => {
      const custom = e as CustomEvent<AssumedTenant | null>;
      if (custom.detail !== undefined) {
        setAssumed(custom.detail);
      } else {
        setAssumed(getAssumedTenant());
      }
    };

    window.addEventListener(EVENT_NAME, handler);
    window.addEventListener("storage", handler);
    return () => {
      window.removeEventListener(EVENT_NAME, handler);
      window.removeEventListener("storage", handler);
    };
  }, []);

  const assumeTenant = useCallback((tenant: AssumedTenant) => {
    setAssumedTenant(tenant);
    setAssumed(tenant);
  }, []);

  const exitAssumeTenant = useCallback(() => {
    clearAssumedTenant();
    setAssumed(null);
  }, []);

  return {
    assumedTenant,
    assumeTenant,
    exitAssumeTenant,
  };
}
