import { mount } from "svelte";
import DocPage from "./DocPage.svelte";
import "../lib/markdown.css";

const app = mount(DocPage, { target: document.getElementById("app")! });
export default app;
