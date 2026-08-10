(function () {
    "use strict";

    const wizard = document.querySelector("[data-wizard]");
    if (!wizard) return;

    const panels = Array.from(wizard.querySelectorAll("[data-wizard-panel]"));
    const stepButtons = Array.from(wizard.querySelectorAll("[data-step-target]"));
    const form = wizard.querySelector("form");
    let current = 1;

    function field(name) { return form && form.elements.namedItem(name); }
    function value(name, fallback) {
        const control = field(name);
        return control && control.value !== "" ? control.value : fallback;
    }

    function lines(raw) {
        return String(raw || "").split(/\r?\n/).map((item) => item.trim()).filter(Boolean);
    }

    function uniqueQueries() {
        const seen = new Set();
        const duplicate = [];
        const unique = [];
        lines(value("keywords", "")).forEach((query) => {
            const key = query.toLocaleLowerCase().replace(/\s+/g, " ");
            if (seen.has(key)) duplicate.push(query);
            else { seen.add(key); unique.push(query); }
        });
        return { unique, duplicate };
    }

    function locations() {
        const items = lines(value("locations", ""));
        return items.length ? items : [value("location_label", "San Francisco, California")];
    }

    function setStep(step, focusHeading) {
        current = Math.max(1, Math.min(panels.length, Number(step) || 1));
        panels.forEach((panel) => { panel.hidden = Number(panel.dataset.wizardPanel) !== current; });
        stepButtons.forEach((button) => {
            const selected = Number(button.dataset.stepTarget) === current;
            if (selected) button.setAttribute("aria-current", "step");
            else button.removeAttribute("aria-current");
        });
        const progress = wizard.querySelector("[data-wizard-progress]");
        if (progress) { progress.value = current; progress.textContent = current + " of " + panels.length; }
        if (focusHeading) {
            const heading = panels[current - 1] && panels[current - 1].querySelector("h2");
            if (heading) { heading.tabIndex = -1; heading.focus(); }
        }
        if (current === panels.length) updateReview();
    }

    function setText(selector, text) {
        const target = wizard.querySelector(selector);
        if (target) target.textContent = text;
    }

    function estimate() {
        const queryCount = Math.max(1, uniqueQueries().unique.length);
        const locationCount = Math.max(1, locations().length);
        const radiusKm = Math.max(0, Number(value("radius", 10000)) / 1000);
        const gridKm = Math.max(.1, Number(value("grid_cell_km", 2.5)));
        const fastMode = Boolean(field("fastmode") && field("fastmode").checked);
        const usesGrid = !fastMode && field("geography_mode") && field("geography_mode").value !== "point";
        const cellsPerLocation = usesGrid ? Math.max(1, Math.pow(Math.ceil((radiusKm * 2) / gridKm), 2)) : 1;
        const cells = cellsPerLocation * locationCount;
        const tasks = queryCount * cells;
        const depth = Math.max(1, Number(value("depth", 10)));
        const concurrency = Math.max(1, Number(value("concurrency", 4)));
        const browserCapacity = Math.max(1, Number(value("browser_pool_size", 2)) * Number(value("pages_per_browser", 2)));
        const effectiveConcurrency = Math.max(1, Math.min(concurrency, browserCapacity));
        const websiteFactor = field("enrich_website") && field("enrich_website").checked ? 1.25 : 1;
        const emailFactor = field("email") && field("email").checked ? 1.35 : 1;
        const minutes = Math.max(3, Math.ceil((tasks * Math.max(1, depth / 5) * .7 * websiteFactor * emailFactor) / effectiveConcurrency));
        return { queryCount, locationCount, cells, tasks, minutes };
    }

    function durationMinutes(raw) {
        const match = String(raw || "").trim().match(/^(\d+(?:\.\d+)?)(ms|s|m|h)$/i);
        if (!match) return 0;
        const amount = Number(match[1]);
        const unit = match[2].toLowerCase();
        if (unit === "h") return amount * 60;
        if (unit === "m") return amount;
        if (unit === "s") return amount / 60;
        return amount / 60000;
    }

    function updatePreview() {
        const result = uniqueQueries();
        const list = wizard.querySelector("[data-query-preview]");
        if (list) {
            list.replaceChildren();
            result.unique.forEach((query) => {
                const item = document.createElement("li");
                item.textContent = query;
                list.appendChild(item);
            });
        }
        setText("[data-query-count]", String(result.unique.length));
        setText("[data-duplicate-count]", String(result.duplicate.length));
        const stats = estimate();
        setText("[data-estimate-locations]", String(stats.locationCount));
        setText("[data-estimate-queries]", String(stats.queryCount));
        setText("[data-estimate-cells]", String(stats.cells));
        setText("[data-estimate-tasks]", String(stats.tasks));
        setText("[data-estimate-runtime]", "~" + stats.minutes + " min");
    }

    function updateReview() {
        updatePreview();
        const stats = estimate();
        setText("[data-review-name]", value("name", "Untitled scrape"));
        setText("[data-review-location]", value("location_label", "San Francisco, California"));
        setText("[data-review-coordinates]", value("latitude", "37.7749") + ", " + value("longitude", "-122.4194"));
        const fastMode = Boolean(field("fastmode") && field("fastmode").checked);
        setText("[data-review-radius]", Number(value("radius", 10000)).toLocaleString() + " m " + (fastMode ? "strict radius" : "grid extent"));
        setText("[data-review-grid]", fastMode ? "Not used in Fast Mode" : value("grid_cell_km", "2.5") + " km");
        setText("[data-review-tasks]", String(stats.tasks));
        setText("[data-review-runtime]", value("maxtime", "60m"));
        const warning = wizard.querySelector("[data-estimate-warning]");
        if (warning) {
            const messages = [];
            if (stats.tasks > 1000) messages.push("This configuration creates more than 1,000 tasks. Consider a larger grid cell or fewer queries.");
            if (Number(value("concurrency", 2)) > (navigator.hardwareConcurrency || 4)) messages.push("Concurrency exceeds the browser's reported CPU count.");
            if (durationMinutes(value("maxtime", "60m")) < stats.minutes) messages.push("The runtime limit may stop this job before all estimated tasks finish. The job will finish as Partial and keep its checkpointed results.");
            warning.hidden = messages.length === 0;
            warning.textContent = messages.join(" ");
        }
    }

    function applySanFranciscoPreset() {
        const values = {
            name: "San Francisco dentists",
            keywords: "dentists in San Francisco\ndental clinics in San Francisco",
            location_label: "San Francisco, California, United States",
            locations: "San Francisco, California, United States",
            latitude: "37.7749",
            longitude: "-122.4194",
            radius: "10000",
            zoom: "12",
            grid_cell_km: "2.5",
            maxtime: "60m"
        };
        Object.keys(values).forEach((name) => { const control = field(name); if (control) control.value = values[name]; });
        updatePreview();
        if (window.GMapsApp) window.GMapsApp.toast("San Francisco dentist settings applied.", "success");
    }

    function applyPerformancePreset(preset) {
        if (preset === "custom") return;
        const presets = {
            fast: { depth: 3, concurrency: 6, browser_pool_size: 2, pages_per_browser: 3, maxtime: "15m" },
            balanced: { depth: 10, concurrency: 4, browser_pool_size: 2, pages_per_browser: 2, maxtime: "60m" },
            deep: { depth: 20, concurrency: 2, browser_pool_size: 2, pages_per_browser: 1, maxtime: "120m" }
        };
        const selected = presets[preset] || presets.balanced;
        Object.keys(selected).forEach((name) => { const control = field(name); if (control) control.value = selected[name]; });
        updatePreview();
    }

    async function loadTextFile(input, targetName) {
        const file = input.files && input.files[0];
        if (!file) return;
        if (file.size > 2 * 1024 * 1024) { if (window.GMapsApp) window.GMapsApp.toast("Files must be 2 MB or smaller.", "error"); input.value = ""; return; }
        const text = await file.text();
        const target = field(targetName);
        if (!target) return;
        const parsed = file.name.toLowerCase().endsWith(".csv")
            ? lines(text).map((line) => line.split(",")[0].replace(/^"|"$/g, ""))
            : lines(text);
        target.value = [target.value.trim(), parsed.join("\n")].filter(Boolean).join("\n");
        updatePreview();
    }

    wizard.addEventListener("click", (event) => {
        const trigger = event.target.closest("[data-action]");
        if (!trigger) return;
        if (trigger.dataset.action === "wizard-step") { event.preventDefault(); setStep(trigger.dataset.stepTarget, true); }
        else if (trigger.dataset.action === "wizard-next") { event.preventDefault(); setStep(current + 1, true); }
        else if (trigger.dataset.action === "wizard-back") { event.preventDefault(); setStep(current - 1, true); }
        else if (trigger.dataset.action === "use-san-francisco-preset") { event.preventDefault(); applySanFranciscoPreset(); }
        else if (trigger.dataset.action === "preview-queries") { event.preventDefault(); updatePreview(); }
    });

    wizard.addEventListener("change", (event) => {
        if (event.target.matches("[data-keywords-file]")) loadTextFile(event.target, "keywords");
        else if (event.target.matches("[data-locations-file]")) loadTextFile(event.target, "locations");
        else if (event.target.name === "performance_preset") applyPerformancePreset(event.target.value);
        else updatePreview();
    });
    wizard.addEventListener("input", (event) => {
        if (["keywords", "locations", "radius", "grid_cell_km", "depth", "concurrency", "browser_pool_size", "pages_per_browser", "maxtime"].includes(event.target.name)) updatePreview();
    });

    panels.forEach((panel, index) => { panel.hidden = index !== 0; });
    setStep(1, false);
    updatePreview();
})();
