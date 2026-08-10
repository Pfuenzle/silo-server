import { createRoot } from "react-dom/client";
import "@fontsource-variable/noto-sans-jp";
import "@fontsource-variable/noto-sans-kr";
import App from "./App";
import { installPreloadErrorReload } from "./lib/reloadOnPreloadError";
import "./app.css";

if (import.meta.env.DEV && import.meta.env.VITE_DISABLE_REACT_DEVTOOLS !== "1") {
  void import("react-grab");
  void import("react-scan");
}

installPreloadErrorReload();

const root = document.getElementById("root");
if (root === null) throw new Error("Root element #root not found");
createRoot(root).render(<App />);
