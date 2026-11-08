// Applied before first paint so the page never flashes the wrong theme.
// Same storage key as src/components/theme-provider.tsx.
try {
  var t = localStorage.getItem("theme") || "system"
  var dark = t === "dark" || (t === "system" && matchMedia("(prefers-color-scheme: dark)").matches)
  document.documentElement.classList.add(dark ? "dark" : "light")
} catch (e) {}
