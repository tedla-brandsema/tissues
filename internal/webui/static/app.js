(() => {
  document.addEventListener("click", (event) => {
    const disclosure = event.target.closest("[data-disclosure]");
    if (disclosure) {
      const branch = disclosure.closest("li").querySelector(":scope > [data-branch]");
      const expanded = disclosure.getAttribute("aria-expanded") === "true";
      disclosure.setAttribute("aria-expanded", String(!expanded));
      disclosure.querySelector("span").textContent = expanded ? "\u25b8" : "\u25be";
      branch.hidden = expanded;
      return;
    }
    const opener = event.target.closest("[data-dialog-open]");
    if (opener) {
      const dialog = document.getElementById(opener.dataset.dialogOpen);
      dialog.showModal();
      const target = dialog.querySelector("[data-autofocus]");
      if (target) target.focus();
      return;
    }
    const closer = event.target.closest("[data-dialog-close]");
    if (closer) closer.closest("dialog").close();
  });
  document.addEventListener("input", (event) => {
    if (!event.target.matches("[data-choice-filter]")) return;
    const query = event.target.value.trim().toLowerCase();
    event.target.closest("dialog").querySelectorAll("[data-choice]").forEach((choice) => {
      choice.hidden = !choice.dataset.title.toLowerCase().includes(query);
    });
  });
})();
