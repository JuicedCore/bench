(() => {
  const root = document.documentElement;
  const stored = localStorage.getItem("bench-theme");
  if (stored === "light" || stored === "dark") root.setAttribute("data-theme", stored);

  function effectiveTheme() {
    const t = root.getAttribute("data-theme");
    if (t === "dark" || t === "light") return t;
    return matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  }

  function label(btn) {
    const t = root.getAttribute("data-theme");
    btn.textContent = t === "dark" ? "Light" : t === "light" ? "Dark" : "Theme";
  }

  document.querySelectorAll(".theme-toggle").forEach((btn) => {
    label(btn);
    btn.addEventListener("click", () => {
      const cur = root.getAttribute("data-theme");
      const next =
        cur === "dark" ? "light" :
        cur === "light" ? "dark" :
        (matchMedia("(prefers-color-scheme: dark)").matches ? "light" : "dark");
      root.setAttribute("data-theme", next);
      localStorage.setItem("bench-theme", next);
      label(btn);
      window.dispatchEvent(new CustomEvent("bench-theme", { detail: { theme: next } }));
    });
  });

  window.benchTheme = effectiveTheme;
})();
