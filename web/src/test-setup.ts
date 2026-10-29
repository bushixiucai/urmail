import { afterEach } from "vitest";
import { configure } from "@testing-library/dom";

configure({ asyncUtilTimeout: 10000 });

afterEach(() => {
  if (typeof window !== "undefined") {
    try {
      window.sessionStorage?.clear();
      window.localStorage?.clear();
    } catch {
      // ignore
    }
  }
});
