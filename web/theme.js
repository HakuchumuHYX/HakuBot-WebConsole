"use strict";

// Follows the system unless the viewer picked the other theme; app.js clears
// the saved choice again when it matches the system.
document.documentElement.setAttribute(
  "data-theme",
  localStorage.getItem("webconsole_theme")
    || (matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark"),
);
