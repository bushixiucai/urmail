import { useQuery } from "@tanstack/react-query";
import { request } from "@/lib/api/client";

export interface HealthResponse {
  status: string;
  version: string;
  db?: string;
}

/** Fetch the service health. */
export function useHealth() {
  return useQuery({
    queryKey: ["health"],
    queryFn: () => request<HealthResponse>("/api/v1/healthz"),
    retry: 1,
    refetchInterval: 30_000,
  });
}
