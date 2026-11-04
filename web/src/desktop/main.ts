import { mount } from "svelte";
import Desktop from "./Desktop.svelte";
import "./desktop.css";
import "../lib/markdown.css";

const app = mount(Desktop, { target: document.getElementById("app")! });
export default app;
