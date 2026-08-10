(function () {
    "use strict";

    const explorer = document.querySelector("[data-map-explorer]");
    if (!explorer) return;
    const canvas = explorer.querySelector("[data-map-canvas]");
    const form = explorer.querySelector("[data-map-preview-form]");
    let zoom = 1;

    function renderGrid(payload) {
        if (!canvas) return;
        const cells = payload.cells || (payload.data && payload.data.cells) || [];
        const svgNS = "http://www.w3.org/2000/svg";
        let svg = canvas.querySelector("svg");
        if (!svg) { svg = document.createElementNS(svgNS, "svg"); canvas.appendChild(svg); }
        svg.replaceChildren();
        svg.setAttribute("viewBox", "0 0 1000 620");
        const columns = Math.max(1, Math.ceil(Math.sqrt(cells.length || 1)));
        const rows = Math.max(1, Math.ceil((cells.length || 1) / columns));
        const cellWidth = 880 / columns;
        const cellHeight = 500 / rows;
        (cells.length ? cells : [{ id: "preview", state: "waiting" }]).forEach((cell, index) => {
            const rect = document.createElementNS(svgNS, "rect");
            rect.setAttribute("x", String(60 + (index % columns) * cellWidth));
            rect.setAttribute("y", String(60 + Math.floor(index / columns) * cellHeight));
            rect.setAttribute("width", String(Math.max(2, cellWidth - 3)));
            rect.setAttribute("height", String(Math.max(2, cellHeight - 3)));
            rect.setAttribute("rx", "4");
            rect.setAttribute("fill", cell.state === "completed" ? "#3aa778" : cell.state === "failed" ? "#d94d5b" : cell.state === "running" ? "#477ae8" : "rgba(100,112,135,.25)");
            rect.setAttribute("stroke", "rgba(23,32,51,.45)");
            rect.dataset.cellId = cell.id || String(index + 1);
            const title = document.createElementNS(svgNS, "title");
            title.textContent = "Cell " + (cell.number || index + 1) + ": " + (cell.state || "waiting");
            rect.appendChild(title);
            svg.appendChild(rect);
        });
        canvas.classList.add("map-ready");
        const count = explorer.querySelector("[data-map-cell-count]");
        if (count) count.textContent = String(cells.length || 1);
    }

    function applyZoom(next) {
        zoom = Math.max(.6, Math.min(3, next));
        const svg = canvas && canvas.querySelector("svg");
        if (!svg) return;
        svg.style.transform = "scale(" + zoom + ")";
        svg.style.transformOrigin = "center";
        svg.setAttribute("aria-label", "Map preview at " + Math.round(zoom * 100) + "% zoom");
    }

    explorer.addEventListener("click", (event) => {
        const trigger = event.target.closest("[data-action]");
        if (!trigger) return;
        const action = trigger.dataset.action;
        if (action === "map-zoom-in") { event.preventDefault(); applyZoom(zoom + .2); }
        else if (action === "map-zoom-out") { event.preventDefault(); applyZoom(zoom - .2); }
        else if (action === "map-fit") { event.preventDefault(); applyZoom(1); }
        else if (action === "map-shape") {
            event.preventDefault();
            const shape = trigger.dataset.shape || "bbox";
            const target = form && form.querySelector("[data-geometry-type]");
            if (target) target.value = shape;
            explorer.querySelectorAll('[data-action="map-shape"]').forEach((button) => button.setAttribute("aria-pressed", String(button === trigger)));
            if (window.GMapsApp) window.GMapsApp.toast(shape.charAt(0).toUpperCase() + shape.slice(1) + " planning selected. Set the centre and extent, or import GeoJSON.", "success");
        }
    });

    explorer.addEventListener("change", (event) => {
        if (event.target.name === "mode") explorer.dataset.mode = event.target.value;
    });

    if (form && window.fetch) {
        form.addEventListener("submit", async (event) => {
            if (event.submitter && event.submitter.dataset.native === "true") return;
            event.preventDefault();
            form.setAttribute("aria-busy", "true");
            try {
                const response = await fetch(form.dataset.endpoint || form.action, { method: "POST", body: new FormData(form), headers: { Accept: "application/json" }, credentials: "same-origin" });
                if (!response.ok) throw new Error("Grid preview failed.");
                renderGrid(await response.json());
            } catch (error) {
                if (window.GMapsApp) window.GMapsApp.toast(error.message, "error");
            } finally { form.removeAttribute("aria-busy"); }
        });
    }

    if (canvas && canvas.querySelector("[data-cell-id], .map-marker")) canvas.classList.add("map-ready");
    const initial = document.getElementById("map-initial-data");
    if (initial) { try { renderGrid(JSON.parse(initial.textContent)); } catch (_) { /* normal fallback remains visible */ } }
})();
