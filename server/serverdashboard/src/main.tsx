import React from "react";
import ReactDOM from "react-dom/client";
import App from "./App";

const savedTheme=localStorage.getItem("tunnelx-theme");
document.documentElement.dataset.theme=savedTheme==="light"||savedTheme==="dark"
  ?savedTheme
  :matchMedia("(prefers-color-scheme: dark)").matches?"dark":"light";

ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
