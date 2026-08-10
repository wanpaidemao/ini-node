import { mount } from "svelte";
import { loadLang } from "./lib/i18n";
import App from "./App.svelte";

loadLang();

mount(App, { target: document.getElementById("app")! });