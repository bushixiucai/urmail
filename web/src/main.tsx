import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "@/App";
import "@/index.css";

const container = document.getElementById("root");
if (!container) {
  throw new Error("root element is missing from index.html");
}

// Tell the index.html boot-failure watchdog that the module graph survived
// (vendor chunks loaded, React mounted). See the inline script in index.html.
declare global {
  interface Window {
    __urmailBooted?: boolean;
  }
}
window.__urmailBooted = true;

createRoot(container).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
