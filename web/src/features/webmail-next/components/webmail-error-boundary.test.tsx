import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import { describe, it, expect, vi, afterEach } from "vitest";
import { useState } from "react";
import { WebmailErrorBoundary } from "./webmail-error-boundary";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string, defaultVal?: string) => defaultVal || key,
  }),
}));

afterEach(() => {
  cleanup();
});

function BuggyComponent({ shouldThrow }: { shouldThrow: boolean }) {
  if (shouldThrow) {
    throw new Error("Simulated rendering crash due to corrupted API payload");
  }
  return <div data-testid="child-content">Normal Content</div>;
}

describe("WebmailErrorBoundary", () => {
  it("renders children when no error occurs", () => {
    render(
      <WebmailErrorBoundary>
        <BuggyComponent shouldThrow={false} />
      </WebmailErrorBoundary>,
    );

    expect(screen.getByTestId("child-content")).toBeTruthy();
    expect(screen.getByText("Normal Content")).toBeTruthy();
  });

  it("catches rendering error, prevents full-page crash, and renders fallback UI", () => {
    // Suppress console.error in test output for intentional errors
    const spy = vi.spyOn(console, "error").mockImplementation(() => {});

    render(
      <WebmailErrorBoundary>
        <BuggyComponent shouldThrow={true} />
      </WebmailErrorBoundary>,
    );

    expect(screen.getByTestId("webmail-error-boundary-fallback")).toBeTruthy();
    expect(screen.getByText("Something went wrong")).toBeTruthy();
    expect(
      screen.getByText(
        "Simulated rendering crash due to corrupted API payload",
      ),
    ).toBeTruthy();

    spy.mockRestore();
  });

  it("allows recovering via onReset / retry button click", () => {
    const spy = vi.spyOn(console, "error").mockImplementation(() => {});
    const onReset = vi.fn();

    function TestHost() {
      const [hasError, setHasError] = useState(true);
      return (
        <WebmailErrorBoundary
          onReset={() => {
            onReset();
            setHasError(false);
          }}
        >
          <BuggyComponent shouldThrow={hasError} />
        </WebmailErrorBoundary>
      );
    }

    render(<TestHost />);

    expect(screen.getByTestId("btn-error-boundary-retry")).toBeTruthy();
    fireEvent.click(screen.getByTestId("btn-error-boundary-retry"));

    expect(onReset).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId("child-content")).toBeTruthy();

    spy.mockRestore();
  });

  it("supports custom fallback node or function", () => {
    const spy = vi.spyOn(console, "error").mockImplementation(() => {});

    render(
      <WebmailErrorBoundary
        fallback={({ error }) => (
          <div data-testid="custom-fallback">Custom: {error.message}</div>
        )}
      >
        <BuggyComponent shouldThrow={true} />
      </WebmailErrorBoundary>,
    );

    expect(screen.getByTestId("custom-fallback")).toBeTruthy();
    expect(
      screen.getByText(
        "Custom: Simulated rendering crash due to corrupted API payload",
      ),
    ).toBeTruthy();

    spy.mockRestore();
  });
});
