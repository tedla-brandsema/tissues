import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@tissues/frontend/styles.css";
import "./workspace.css";
import { App } from "./App";

createRoot(document.getElementById("root")!).render(<StrictMode><App /></StrictMode>);
