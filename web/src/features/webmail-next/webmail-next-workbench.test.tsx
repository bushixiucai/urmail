import React from "react";
import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import { describe, it, expect, vi, afterEach } from "vitest";
import { WebmailNextWorkbench } from "./webmail-next-workbench";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

const queryClient = new QueryClient();

afterEach(() => {
  cleanup();
});

describe("WebmailNextWorkbench Layout", () => {
  it("renders header, navigation rail, list, and reading pane", () => {
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <WebmailNextWorkbench />
        </MemoryRouter>
      </QueryClientProvider>
    );
    expect(screen.getByTestId("webmail-header")).toBeTruthy();
    expect(screen.getByTestId("webmail-navigation-rail")).toBeTruthy();
    expect(screen.getByTestId("webmail-message-list")).toBeTruthy();
    expect(screen.getByTestId("webmail-reading-pane")).toBeTruthy();
  });

  it("toggles rail collapse to mini mode", () => {
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <WebmailNextWorkbench />
        </MemoryRouter>
      </QueryClientProvider>
    );
    const rail = screen.getByTestId("webmail-navigation-rail");
    expect(rail.getAttribute("data-collapsed")).toBe("false");

    const toggleBtn = screen.getByTestId("rail-toggle-button");
    fireEvent.click(toggleBtn);
    expect(rail.getAttribute("data-collapsed")).toBe("true");
  });

  it("toggles density mode", () => {
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <WebmailNextWorkbench />
        </MemoryRouter>
      </QueryClientProvider>
    );
    const densityBtn = screen.getByTestId("webmail-density-toggle");
    expect(densityBtn.textContent).toMatch(/compact/i);
    fireEvent.click(densityBtn);
    expect(densityBtn.textContent).toMatch(/comfortable/i);
  });
});
