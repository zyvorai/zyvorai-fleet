// Runs in <head>, before first paint: stored choice, else the OS preference.
(function () {
  var KEY = "zyvor-site-theme", root = document.documentElement, stored = null;
  try { stored = localStorage.getItem(KEY); } catch (e) { /* private mode */ }
  var dark = window.matchMedia && matchMedia("(prefers-color-scheme: dark)").matches;
  var theme = stored === "light" || stored === "dark" ? stored : dark ? "dark" : "light";
  root.setAttribute("data-theme", theme);
  var m = document.querySelector('meta[name="theme-color"]');
  if (m) m.setAttribute("content", theme === "dark" ? "#07080d" : "#ffffff");
  window.__zyvorTheme = { key: KEY };
})();
