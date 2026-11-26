import "./admin/style.css";
import { boot } from "./admin/app";

const root = document.getElementById("app");
if (root)
  boot(root).catch(
    (e) => (root.textContent = `Cannot reach the server: ${e instanceof Error ? e.message : e}`),
  );
