import { Component, type ErrorInfo, type ReactNode } from "react";
import { AlertTriangle, RotateCcw } from "lucide-react";
import { useTranslation } from "react-i18next";

export interface WebmailErrorBoundaryProps {
  children: ReactNode;
  fallback?:
    ReactNode | ((props: { error: Error; reset: () => void }) => ReactNode);
  title?: string;
  description?: string;
  onError?: (error: Error, errorInfo: ErrorInfo) => void;
  onReset?: () => void;
}

interface State {
  hasError: boolean;
  error: Error | null;
}

class ErrorBoundaryClass extends Component<
  WebmailErrorBoundaryProps & {
    defaultTitle?: string;
    defaultDescription?: string;
    retryLabel?: string;
  },
  State
> {
  constructor(
    props: WebmailErrorBoundaryProps & {
      defaultTitle?: string;
      defaultDescription?: string;
      retryLabel?: string;
    },
  ) {
    super(props);
    this.state = { hasError: false, error: null };
  }

  static getDerivedStateFromError(error: Error): State {
    return { hasError: true, error };
  }

  componentDidCatch(error: Error, errorInfo: ErrorInfo) {
    if (this.props.onError) {
      this.props.onError(error, errorInfo);
    }
  }

  reset = () => {
    if (this.props.onReset) {
      this.props.onReset();
    }
    this.setState({ hasError: false, error: null });
  };

  render() {
    if (this.state.hasError) {
      if (typeof this.props.fallback === "function") {
        return this.props.fallback({
          error: this.state.error || new Error("Unknown error"),
          reset: this.reset,
        });
      }
      if (this.props.fallback) {
        return this.props.fallback;
      }

      const title =
        this.props.title || this.props.defaultTitle || "Something went wrong";
      const desc =
        this.props.description ||
        this.props.defaultDescription ||
        "An unexpected error occurred while rendering this section. Your data is safe.";
      const retryText = this.props.retryLabel || "Try Again";

      return (
        <div
          data-testid="webmail-error-boundary-fallback"
          className="p-6 bg-[#FAF9F5] border border-[#FECACA] rounded-xl text-center space-y-3 m-4 shadow-xs"
        >
          <div className="w-10 h-10 mx-auto rounded-full bg-[#FEF2F2] flex items-center justify-center text-[#DC2626]">
            <AlertTriangle className="w-5 h-5" />
          </div>
          <div>
            <h4 className="text-sm font-bold text-[#18181B]">{title}</h4>
            <p className="text-xs text-[#52525B] mt-1 max-w-md mx-auto">
              {desc}
            </p>
            {this.state.error && (
              <p className="text-xs font-mono text-[#991B1B] bg-[#FEE2E2] px-2 py-1 rounded mt-2 inline-block max-w-full truncate">
                {this.state.error.message}
              </p>
            )}
          </div>
          <div>
            <button
              data-testid="btn-error-boundary-retry"
              type="button"
              onClick={this.reset}
              className="inline-flex items-center gap-1.5 px-3 py-1.5 bg-[var(--brand-primary,#2563eb)] text-white text-xs font-semibold rounded-md shadow-xs hover:brightness-105 active:brightness-95 transition-all"
            >
              <RotateCcw className="w-3.5 h-3.5" />
              <span>{retryText}</span>
            </button>
          </div>
        </div>
      );
    }

    return this.props.children;
  }
}

export function WebmailErrorBoundary(props: WebmailErrorBoundaryProps) {
  const { t } = useTranslation();
  return (
    <ErrorBoundaryClass
      {...props}
      defaultTitle={t(
        "webmail_next.error_boundary.title",
        "Something went wrong",
      )}
      defaultDescription={t(
        "webmail_next.error_boundary.description",
        "An unexpected error occurred while rendering this section. Your data is safe.",
      )}
      retryLabel={t("webmail_next.error_boundary.retry", "Try Again")}
    />
  );
}
