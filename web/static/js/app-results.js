(function () {
    "use strict";

    const explorer = document.querySelector("[data-results-explorer]");
    if (!explorer) return;
    const checkboxes = () => Array.from(explorer.querySelectorAll('[name="result_ids"]'));
    const count = explorer.querySelector("[data-selection-count]");
    const bar = explorer.querySelector("[data-selection-bar]");

    function updateSelection() {
        const selected = checkboxes().filter((item) => item.checked);
        if (count) count.textContent = String(selected.length);
        if (bar) bar.hidden = selected.length === 0;
        explorer.querySelectorAll("[data-requires-selection]").forEach((control) => { control.disabled = selected.length === 0; });
        const all = explorer.querySelector('[data-select-all]');
        if (all) { all.checked = selected.length > 0 && selected.length === checkboxes().length; all.indeterminate = selected.length > 0 && selected.length < checkboxes().length; }
    }

    explorer.addEventListener("change", (event) => {
        if (event.target.matches("[data-select-all]")) checkboxes().forEach((item) => { item.checked = event.target.checked; });
        if (event.target.matches('[name="result_ids"], [data-select-all]')) updateSelection();
    });

    explorer.addEventListener("click", async (event) => {
        const trigger = event.target.closest("[data-action]");
        if (!trigger) return;
        if (trigger.dataset.action === "add-filter") {
            event.preventDefault();
            const template = document.getElementById("filter-row-template");
            const rows = explorer.querySelector("[data-filter-rows]");
            if (template && rows) rows.appendChild(template.content.cloneNode(true));
        } else if (trigger.dataset.action === "remove-filter") {
            event.preventDefault();
            const row = trigger.closest(".filter-row");
            if (row) row.remove();
        } else if (trigger.dataset.action === "open-result") {
            if (!window.fetch || !trigger.dataset.endpoint) return;
            event.preventDefault();
            const drawer = document.getElementById("result-drawer");
            const body = drawer && drawer.querySelector("[data-drawer-body]");
            if (!drawer || !body) return;
            body.setAttribute("aria-busy", "true");
            try {
                const response = await fetch(trigger.dataset.endpoint, { headers: { Accept: "text/html" }, credentials: "same-origin" });
                if (!response.ok) throw new Error("Could not load this business.");
                body.innerHTML = await response.text();
                if (typeof drawer.showModal === "function") drawer.showModal(); else drawer.setAttribute("open", "");
            } catch (error) {
                if (window.GMapsApp) window.GMapsApp.toast(error.message, "error");
            } finally { body.removeAttribute("aria-busy"); }
        }
    });
    updateSelection();
})();
