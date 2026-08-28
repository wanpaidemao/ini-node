import { mount } from "svelte";
// Importing the Wails runtime is required: its side-effect modules
// (drag.ts / appregion.ts) register the mouse listeners and non-client
// region tracking that make the frameless titlebar draggable and its
// minimize/maximize/close buttons work. Without this import the CSS
// --wails-draggable / --wails-non-client-region properties are inert.
import "@wailsio/runtime";
import { loadLang } from "./lib/i18n";
import App from "./App.svelte";

loadLang();

mount(App, { target: document.getElementById("app")! });