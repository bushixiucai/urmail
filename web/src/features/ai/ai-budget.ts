export interface AIQuotaEntitlement {
  source: "configured_nonplatform" | "plan" | "unconfigured";
  available: boolean;
  call_limit: number;
  mailbox_call_limit: number;
  concurrency_limit: number;
  period_start: string | null;
  period_end: string | null;
  timezone: string;
  calls_used: number;
  mailbox_calls_used?: number | null;
  calls_remaining: number;
  mailbox_calls_remaining?: number | null;
  concurrent_reserved: number;
}
export interface AIBudgetWindow {
  tokens_used: number;
  tokens_limit: number | null;
  cost_micros_used: number;
  cost_micros_limit: number | null;
}
export interface AIBudgetStatus {
  enabled: boolean;
  mode?: string;
  quota_timezone?: string;
  cost_currency: string;
  daily: AIBudgetWindow;
  monthly: AIBudgetWindow;
  entitlement?: AIQuotaEntitlement;
}
