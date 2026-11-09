import { useEffect, useState } from "react";

const QUERY = "(max-width: 767px)"; // Tailwind md breakpoint = 768px; below md is mobile

/**
 * B2-3（WEBMAIL-BATCH2-SPEC.md §四）：当前视口是否为移动端（< md=768px）。
 * SSR/无 matchMedia 环境恒为 false（桌面为默认渲染形态）。
 */
export function useIsMobileViewport(): boolean {
  const [mobile, setMobile] = useState(() => {
    if (typeof window === "undefined" || !window.matchMedia) return false;
    return window.matchMedia(QUERY).matches;
  });
  useEffect(() => {
    if (typeof window === "undefined" || !window.matchMedia) return;
    const media = window.matchMedia(QUERY);
    const apply = () => setMobile(media.matches);
    apply();
    media.addEventListener("change", apply);
    return () => media.removeEventListener("change", apply);
  }, []);
  return mobile;
}
