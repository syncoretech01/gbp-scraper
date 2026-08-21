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

    function notify(message, kind) {
        if (window.GMapsApp) window.GMapsApp.toast(message, kind || "success");
    }

    function setStatus(selector, message) {
        const status = wizard.querySelector(selector);
        if (status) status.textContent = message;
    }

    // appendUniqueLines adds new query lines to the keywords textarea,
    // skipping exact duplicates of lines that are already present.
    function appendUniqueLines(candidates) {
        const target = field("keywords");
        if (!target) return { added: 0, skipped: 0 };
        const existing = lines(target.value);
        const seen = new Set(existing);
        let added = 0;
        let skipped = 0;
        candidates.map((item) => String(item || "").trim()).filter(Boolean).forEach((line) => {
            if (seen.has(line)) { skipped += 1; return; }
            seen.add(line);
            existing.push(line);
            added += 1;
        });
        if (added) target.value = existing.join("\n");
        updatePreview();
        return { added: added, skipped: skipped };
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

    // --- Wizard modes -------------------------------------------------------
    // Basic exposes only the search, location, and review steps. Advanced adds
    // data fields, enrichment, filters, and performance. GBP Prospecting is
    // Advanced plus the coverage generator, the pipeline preset, and the
    // adaptive-coverage controls. Mode only changes what is *shown*: hidden
    // fields are never disabled, so every step's values still submit and the
    // job that is created is identical to the one Advanced would create.
    const modeStorageKey = "gmaps-wizard-mode";
    const modeInputs = Array.from(wizard.querySelectorAll("[data-wizard-mode-input]"));
    const modeHints = {
        basic: "Just the search, the area, and launch. Saved defaults cover everything else.",
        advanced: "Every option, including data fields, enrichment, filtering, and performance tuning.",
        gbp: "Advanced plus the ZIP coverage generator, the prospecting pipeline preset, and adaptive coverage."
    };
    let mode = "advanced";

    function modeAllows(element) {
        if (element.hasAttribute("data-wizard-gbp")) return mode === "gbp";
        if (element.hasAttribute("data-wizard-advanced")) return mode !== "basic";
        return true;
    }

    function availablePanels() {
        return panels.filter((panel) => panel.dataset.modeHidden !== "true");
    }

    function readStoredMode() {
        try {
            const stored = window.localStorage.getItem(modeStorageKey);
            return modeInputs.some((input) => input.value === stored) ? stored : "";
        } catch (_) {
            return "";
        }
    }

    // ?mode=basic|advanced|gbp and #step-N let another page deep-link straight
    // into the level of detail it is talking about (the GBP field guide, a
    // "resume where you were" link). Both are optional: an unknown value falls
    // back to the stored mode and step 1.
    function requestedMode() {
        const requested = new URLSearchParams(window.location.search).get("mode");
        return modeInputs.some((input) => input.value === requested) ? requested : "";
    }

    function requestedStep() {
        const match = String(window.location.hash || "").match(/^#step-(\d+)$/);
        return match ? Number(match[1]) : 0;
    }

    function applyMode(next, remember) {
        const chosen = modeInputs.some((input) => input.value === next) ? next : "advanced";
        mode = chosen;
        modeInputs.forEach((input) => { input.checked = input.value === chosen; });
        if (remember) {
            try { window.localStorage.setItem(modeStorageKey, chosen); } catch (_) { /* storage may be disabled */ }
        }

        wizard.querySelectorAll("[data-wizard-advanced], [data-wizard-gbp]").forEach((element) => {
            const allowed = modeAllows(element);
            if (element.matches("[data-wizard-panel]")) element.dataset.modeHidden = allowed ? "false" : "true";
            else element.hidden = !allowed;
        });
        // A step button lives in the <li>; hide the list item so the rail does
        // not leave a gap where a step used to be.
        wizard.querySelectorAll(".wizard-steps li").forEach((item) => {
            const panelNumber = item.querySelector("[data-step-target]");
            const panel = panelNumber && wizard.querySelector('[data-wizard-panel="' + panelNumber.dataset.stepTarget + '"]');
            item.hidden = Boolean(panel && panel.dataset.modeHidden === "true");
        });

        // In Basic mode a "next" button can lead somewhere other than the step
        // its label names, so any button that declares a Basic label swaps to
        // it rather than promising a step the mode has removed.
        wizard.querySelectorAll("[data-next-label-basic]").forEach((button) => {
            if (!button.dataset.nextLabelDefault) button.dataset.nextLabelDefault = button.textContent;
            button.textContent = chosen === "basic" ? button.dataset.nextLabelBasic : button.dataset.nextLabelDefault;
        });

        const hint = wizard.querySelector("[data-wizard-mode-hint]");
        if (hint) hint.textContent = modeHints[chosen] || modeHints.advanced;
        if (chosen === "gbp") {
            const coverage = wizard.querySelector("[data-gbp-coverage]");
            if (coverage) coverage.open = true;
        }
        renumberSteps();
        const visible = availablePanels();
        const currentPanel = wizard.querySelector('[data-wizard-panel="' + current + '"]');
        if (!currentPanel || currentPanel.dataset.modeHidden === "true") setStep(visible.length ? visible[0].dataset.wizardPanel : 1, false);
        else setStep(current, false);
    }

    // renumberSteps keeps the visible rail numbered 1..n for the current mode
    // rather than leaving gaps where advanced steps were removed.
    function renumberSteps() {
        let position = 0;
        wizard.querySelectorAll(".wizard-steps li").forEach((item) => {
            if (item.hidden) return;
            position += 1;
            const number = item.querySelector(".step-number");
            if (number) number.textContent = String(position);
        });
    }

    function setStep(step, focusHeading) {
        const visible = availablePanels();
        if (!visible.length) return;
        const requested = Number(step) || 1;
        let target = visible.find((panel) => Number(panel.dataset.wizardPanel) === requested);
        if (!target) {
            // Moving into a step the current mode hides: land on the nearest
            // available step in the direction of travel.
            target = requested > current
                ? visible.find((panel) => Number(panel.dataset.wizardPanel) > current)
                : visible.slice().reverse().find((panel) => Number(panel.dataset.wizardPanel) < current);
            if (!target) target = requested > current ? visible[visible.length - 1] : visible[0];
        }
        current = Number(target.dataset.wizardPanel);
        panels.forEach((panel) => { panel.hidden = panel !== target; });
        stepButtons.forEach((button) => {
            const selected = Number(button.dataset.stepTarget) === current;
            if (selected) button.setAttribute("aria-current", "step");
            else button.removeAttribute("aria-current");
        });
        const position = visible.indexOf(target) + 1;
        const progress = wizard.querySelector("[data-wizard-progress]");
        if (progress) { progress.max = visible.length; progress.value = position; progress.textContent = position + " of " + visible.length; }
        if (focusHeading) {
            const heading = target.querySelector("h2");
            if (heading) { heading.tabIndex = -1; heading.focus(); }
        }
        if (target === visible[visible.length - 1]) updateReview();
    }

    // Several estimate hooks (queries, cells, tasks) appear on both the
    // location step and the pre-flight step, so every match is updated: with
    // querySelector the review tiles silently kept their placeholder zeros.
    function setText(selector, text) {
        wizard.querySelectorAll(selector).forEach((target) => { target.textContent = text; });
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
        // One control ("email") enables both the website audit and contact
        // extraction, so a single factor covers the added network work.
        const enrichmentFactor = field("email") && field("email").checked ? 1.7 : 1;
        const minutes = Math.max(3, Math.ceil((tasks * Math.max(1, depth / 5) * .7 * enrichmentFactor) / effectiveConcurrency));
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

    // A pre-flight row states a fact and its consequence, and turns amber when
    // the operator should look before launching. Tone is carried by the text as
    // well as the colour: the title changes, never just the swatch.
    function setPreflight(key, tone, title, detail) {
        const row = wizard.querySelector("[data-preflight-" + key + "]");
        if (!row) return;
        row.dataset.tone = tone;
        const mark = row.querySelector(".preflight-mark");
        if (mark) mark.textContent = tone === "ok" ? "✓" : "!";
        setText("[data-preflight-" + key + "-title]", title);
        setText("[data-preflight-" + key + "-detail]", detail);
    }

    function selectedText(name, fallback) {
        const control = field(name);
        if (!control || !control.options || control.selectedIndex < 0) return fallback;
        return control.options[control.selectedIndex].textContent.trim() || fallback;
    }

    function updatePreflight(stats) {
        const queries = uniqueQueries().unique.length;
        setPreflight("queries", queries > 0 ? "ok" : "warn",
            queries > 0 ? "Queries ready" : "No queries yet",
            queries + (queries === 1 ? " unique query" : " unique queries"));

        const fastMode = Boolean(field("fastmode") && field("fastmode").checked);
        const savedArea = value("saved_area_id", "");
        setPreflight("area", "ok",
            savedArea ? "Saved area snapshot" : (fastMode ? "Strict radius" : "Grid coverage"),
            Number(value("radius", 10000)).toLocaleString() + " m" + (fastMode ? " radius" : " extent, " + value("grid_cell_km", "2.5") + " km cells"));

        const limit = durationMinutes(value("maxtime", "60m"));
        const fits = limit >= stats.minutes;
        setPreflight("budget", fits ? "ok" : "warn",
            fits ? "Runtime budget fits" : "Runtime limit is tight",
            "~" + stats.minutes + " min estimated against a " + value("maxtime", "60m") + " limit");

        const enrichmentOn = Boolean(field("email") && field("email").checked);
        setPreflight("enrichment", "ok",
            enrichmentOn ? "Website enrichment on" : "Enrichment off",
            enrichmentOn ? selectedText("enrichment_scope", "Homepage only") + ", up to " + value("enrichment_max_pages", "3") + " pages per site" : "Maps fields only");
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
        const enrichmentOn = Boolean(field("email") && field("email").checked);
        setText("[data-review-enrichment]", enrichmentOn ? "Website audit and contacts — " + selectedText("enrichment_scope", "homepage") : "Off");
        setText("[data-review-proxy]", selectedText("proxy_pool_id", "Direct connection"));
        setText("[data-review-fields]", fieldSelectionSummary());
        setText("[data-review-filters]", filterSummary());
        setText("[data-review-incremental]", selectedText("incremental_mode", "Full collection"));
        updatePreflight(stats);
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

    // --- Step 3: data fields -----------------------------------------------
    // The checkboxes carry the same keys web/job_fields.go validates. The
    // toggle is what decides whether a selection is submitted at all: with it
    // off the form posts no narrowing selection, which is byte-identical to a
    // job created before this step existed.

    function fieldToggle() { return wizard.querySelector("[data-fields-toggle]"); }

    function fieldCheckboxes() {
        return Array.from(wizard.querySelectorAll('[data-field-selection] input[name="fields"]'));
    }

    function selectedFieldKeys() {
        return fieldCheckboxes().filter((box) => box.checked).map((box) => box.value);
    }

    function syncFieldSelection() {
        const toggle = fieldToggle();
        const panel = wizard.querySelector("[data-field-selection]");
        if (!panel) return;
        const active = Boolean(toggle && toggle.checked);
        panel.hidden = !active;
        // A hidden fieldset must not submit values, or an unticked toggle
        // would still narrow the job.
        fieldCheckboxes().forEach((box) => {
            if (box.hasAttribute("data-field-required")) return;
            box.disabled = !active;
        });
        const total = fieldCheckboxes().length;
        const chosen = selectedFieldKeys().length;
        setStatus("[data-field-summary]", active
            ? chosen + " of " + total + " fields retained for display and export."
            : "All " + total + " captured fields are retained.");
    }

    function setAllFields(keys) {
        const wanted = keys === null ? null : new Set(keys);
        fieldCheckboxes().forEach((box) => {
            if (box.hasAttribute("data-field-required")) { box.checked = true; return; }
            box.checked = wanted === null ? true : wanted.has(box.value);
        });
        syncFieldSelection();
    }

    const CORE_FIELD_KEYS = [
        "name", "category", "additional_categories", "address", "phone", "website", "domain",
        "coordinates", "rating", "reviews", "business_status",
        "place_id", "cid", "data_id", "input_id", "source_query", "source_cell"
    ];

    function fieldSelectionSummary() {
        const toggle = fieldToggle();
        if (!toggle || !toggle.checked) return "All captured fields";
        const chosen = selectedFieldKeys();
        const total = fieldCheckboxes().length;
        if (chosen.length >= total) return "All captured fields";
        return chosen.length + " of " + total + " fields (display and export only)";
    }

    // --- Step 5: post-collection filters ------------------------------------

    function filterSummary() {
        const parts = [];
        const ratingMin = value("filter_rating_min", "");
        const ratingMax = value("filter_rating_max", "");
        if (ratingMin || ratingMax) parts.push("rating " + (ratingMin || "0") + "–" + (ratingMax || "5"));
        const reviewsMin = value("filter_reviews_min", "");
        const reviewsMax = value("filter_reviews_max", "");
        if (reviewsMin || reviewsMax) parts.push("reviews " + (reviewsMin || "0") + "–" + (reviewsMax || "any"));
        const include = splitCategoryList(value("filter_include_categories", ""));
        if (include.length) parts.push(include.length + " included " + (include.length === 1 ? "category" : "categories"));
        const exclude = splitCategoryList(value("filter_exclude_categories", ""));
        if (exclude.length) parts.push(exclude.length + " excluded " + (exclude.length === 1 ? "category" : "categories"));
        const statuses = Array.from(wizard.querySelectorAll('input[name="filter_status"]:checked')).map((box) => box.value);
        if (statuses.length) parts.push("status " + statuses.join(", "));
        const claimed = value("filter_claimed", "any");
        if (claimed === "claimed") parts.push("claimed only");
        else if (claimed === "unclaimed") parts.push("unclaimed only");
        if (value("filter_name_contains", "")) parts.push('name contains "' + value("filter_name_contains", "") + '"');
        if (value("filter_name_excludes", "")) parts.push('name excludes "' + value("filter_name_excludes", "") + '"');
        if (!parts.length) return "None";
        return parts.join("; ") + " — applied after collection";
    }

    const INCREMENTAL_NOTES = {
        "": "Full collection keeps every business this run observes.",
        new_only: "Maps has no “only new listings” query, so the plan still visits every cell. This mode keeps the run's view to businesses this job saw first.",
        new_changed: "Maps has no “only changed listings” query, so the plan still visits every cell. This mode keeps the run's view to businesses this job discovered or changed.",
        volatile_fields: "Maps has no partial-record fetch, so the full listing is still collected. This mode keeps the run's view to businesses whose phone, website, address, category, rating, review count, hours, or status moved.",
        stale_contacts: "Collection is unchanged. Only the local website audit is narrowed: a business audited more recently than the re-audit window is skipped."
    };

    function syncIncrementalNote() {
        const mode = value("incremental_mode", "");
        setStatus("[data-incremental-note]", INCREMENTAL_NOTES[mode] || INCREMENTAL_NOTES[""]);
        // "Re-enrich stale contacts" and "re-audit everything" are opposites.
        const force = field("enrichment_force_reaudit");
        if (force && mode === "stale_contacts") force.checked = false;
    }

    function splitCategoryList(raw) {
        return String(raw || "").split(/[\r\n,]+/).map((item) => item.trim()).filter(Boolean);
    }

    // --- Step 1: business category picker and reusable groups ---------------
    // Both hydrate from the local API. The picker stays hidden until the
    // category endpoint answers, and the groups block stays hidden until the
    // group endpoint answers, so an older database shows no dead controls.

    let categoryCatalogue = [];
    const chosenCategories = new Set();

    function categoryPicker() { return wizard.querySelector("[data-category-picker]"); }

    async function loadCategoryCatalogue() {
        const picker = categoryPicker();
        if (!picker) return;
        try {
            const response = await fetch("/api/v1/business-categories", {
                credentials: "same-origin", headers: { "Accept": "application/json" }
            });
            if (!response.ok) return;
            const payload = await response.json();
            categoryCatalogue = Array.isArray(payload.data) ? payload.data : [];
            if (!categoryCatalogue.length) return;
            const sectors = (payload.meta && Array.isArray(payload.meta.sectors)) ? payload.meta.sectors : [];
            const select = wizard.querySelector("[data-category-sector]");
            if (select) {
                sectors.forEach((sector) => {
                    const option = document.createElement("option");
                    option.value = sector;
                    option.textContent = sector;
                    select.appendChild(option);
                });
            }
            picker.hidden = false;
            renderCategoryOptions();
            loadCategoryGroups();
        } catch (_) {
            // Leaving the picker hidden is the correct degraded state.
        }
    }

    function renderCategoryOptions() {
        const host = wizard.querySelector("[data-category-options]");
        if (!host) return;
        const sectorControl = wizard.querySelector("[data-category-sector]");
        const searchControl = wizard.querySelector("[data-category-search]");
        const sector = sectorControl ? sectorControl.value : "";
        const needle = (searchControl ? searchControl.value : "").trim().toLocaleLowerCase();
        host.replaceChildren();
        let shown = 0;
        categoryCatalogue.forEach((category) => {
            if (sector && category.sector !== sector) return;
            if (needle && category.name.toLocaleLowerCase().indexOf(needle) === -1) return;
            shown += 1;
            const chip = document.createElement("button");
            chip.type = "button";
            chip.className = "chip";
            chip.textContent = category.name;
            chip.dataset.categoryName = category.name;
            chip.setAttribute("aria-pressed", chosenCategories.has(category.name) ? "true" : "false");
            host.appendChild(chip);
        });
        if (!shown) {
            const empty = document.createElement("p");
            empty.className = "field-hint";
            empty.textContent = "No bundled category matches that search. Free text still works in the box above.";
            host.appendChild(empty);
        }
        setStatus("[data-category-status]", chosenCategories.size
            ? chosenCategories.size + " selected."
            : "Select categories, then add them to the combination generator.");
    }

    function toggleCategoryChip(chip) {
        const name = chip.dataset.categoryName;
        if (chosenCategories.has(name)) chosenCategories.delete(name);
        else chosenCategories.add(name);
        chip.setAttribute("aria-pressed", chosenCategories.has(name) ? "true" : "false");
        setStatus("[data-category-status]", chosenCategories.size + " selected.");
    }

    function appendCategories(names) {
        const target = wizard.querySelector("[data-combo-categories]");
        if (!target) return 0;
        const existing = lines(target.value);
        const seen = new Set(existing.map((item) => item.toLocaleLowerCase()));
        let added = 0;
        names.forEach((name) => {
            const key = name.toLocaleLowerCase();
            if (seen.has(key)) return;
            seen.add(key);
            existing.push(name);
            added += 1;
        });
        target.value = existing.join("\n");
        return added;
    }

    function insertSelectedCategories() {
        if (!chosenCategories.size) { notify("Select at least one category first.", "error"); return; }
        const added = appendCategories(Array.from(chosenCategories));
        setStatus("[data-category-status]", added + " added to the categories box; " + (chosenCategories.size - added) + " already present.");
        notify(added + " categories added.", "success");
    }

    function clearSelectedCategories() {
        chosenCategories.clear();
        renderCategoryOptions();
    }

    async function loadCategoryGroups() {
        const host = wizard.querySelector("[data-category-groups]");
        const select = wizard.querySelector("[data-category-group-picker]");
        if (!host || !select) return;
        try {
            const response = await fetch("/api/v1/category-groups", {
                credentials: "same-origin", headers: { "Accept": "application/json" }
            });
            if (!response.ok) return;
            const payload = await response.json();
            const groups = Array.isArray(payload.data) ? payload.data : [];
            select.replaceChildren();
            const placeholder = document.createElement("option");
            placeholder.value = "";
            placeholder.textContent = "Choose a saved group…";
            select.appendChild(placeholder);
            groups.forEach((group) => {
                const option = document.createElement("option");
                option.value = group.id;
                option.textContent = group.name + " (" + (group.categories || []).length + ")";
                option.dataset.categories = (group.categories || []).join("\n");
                select.appendChild(option);
            });
            host.hidden = false;
        } catch (_) {
            // Groups need durable settings storage; hiding them is correct.
        }
    }

    function selectedCategoryGroupOption() {
        const select = wizard.querySelector("[data-category-group-picker]");
        if (!select || !select.value) return null;
        return select.options[select.selectedIndex];
    }

    async function insertCategoryGroup() {
        const option = selectedCategoryGroupOption();
        if (!option) { notify("Choose a saved group first.", "error"); return; }
        const names = String(option.dataset.categories || "").split("\n").filter(Boolean);
        const added = appendCategories(names);
        setStatus("[data-category-status]", added + " categories inserted from “" + option.textContent + "”.");
        await fetch("/api/v1/category-groups/" + encodeURIComponent(option.value) + "/use", {
            method: "POST", credentials: "same-origin",
            headers: { "Accept": "application/json", "X-CSRF-Token": value("csrf_token", "") }
        }).catch(() => undefined);
    }

    async function saveCategoryGroup(trigger) {
        const nameInput = wizard.querySelector("[data-category-group-name]");
        const name = nameInput ? nameInput.value.trim() : "";
        const target = wizard.querySelector("[data-combo-categories]");
        const categories = chosenCategories.size ? Array.from(chosenCategories) : lines(target ? target.value : "");
        if (!name) { notify("Name the group first.", "error"); return; }
        if (!categories.length) { notify("Select or type at least one category first.", "error"); return; }
        trigger.disabled = true;
        try {
            const response = await fetch("/api/v1/category-groups", {
                method: "POST", credentials: "same-origin",
                headers: { "Accept": "application/json", "Content-Type": "application/json", "X-CSRF-Token": value("csrf_token", "") },
                body: JSON.stringify({ name: name, categories: categories })
            });
            const payload = await response.json().catch(() => ({}));
            if (!response.ok) throw new Error((payload.error && payload.error.message) || "Could not save the category group");
            if (nameInput) nameInput.value = "";
            await loadCategoryGroups();
            notify("Category group saved.", "success");
        } catch (error) {
            notify(error.message || "Could not save the category group.", "error");
        } finally {
            trigger.disabled = false;
        }
    }

    async function deleteCategoryGroup(trigger) {
        const option = selectedCategoryGroupOption();
        if (!option) { notify("Choose a saved group first.", "error"); return; }
        if (!window.confirm("Delete the category group “" + option.textContent + "”?")) return;
        trigger.disabled = true;
        try {
            const response = await fetch("/api/v1/category-groups/" + encodeURIComponent(option.value) + "/delete", {
                method: "POST", credentials: "same-origin",
                headers: { "Accept": "application/json", "X-CSRF-Token": value("csrf_token", "") }
            });
            if (!response.ok) throw new Error("Could not delete the category group");
            await loadCategoryGroups();
            notify("Category group deleted.", "success");
        } catch (error) {
            notify(error.message || "Could not delete the category group.", "error");
        } finally {
            trigger.disabled = false;
        }
    }

    // --- Parameterised configurations ---------------------------------------
    // The server owns the expansion rules, so the preview asks it rather than
    // reimplementing them here and drifting.

    async function previewParameters(trigger) {
        const categories = splitCategoryList(value("parameter_categories", ""));
        const locations = splitCategoryList(value("parameter_locations", ""));
        const list = wizard.querySelector("[data-parameter-preview]");
        if (list) list.replaceChildren();
        if (!categories.length || !locations.length) {
            setStatus("[data-parameter-status]", "Add at least one category and one location.");
            return;
        }
        trigger.disabled = true;
        try {
            const response = await fetch("/api/v1/templates/parameters/preview", {
                method: "POST", credentials: "same-origin",
                headers: { "Accept": "application/json", "Content-Type": "application/json", "X-CSRF-Token": value("csrf_token", "") },
                body: JSON.stringify({ categories: categories, locations: locations, query_pattern: value("parameter_pattern", "") })
            });
            const payload = await response.json().catch(() => ({}));
            if (!response.ok) throw new Error((payload.error && payload.error.message) || "Could not expand the parameters");
            const queries = Array.isArray(payload.data) ? payload.data : [];
            if (list) {
                queries.forEach((query) => {
                    const item = document.createElement("li");
                    item.textContent = query;
                    list.appendChild(item);
                });
            }
            const total = (payload.meta && payload.meta.count) || queries.length;
            setStatus("[data-parameter-status]", total + " queries will be generated on every run" +
                (payload.meta && payload.meta.truncated ? "; showing the first " + queries.length + "." : "."));
        } catch (error) {
            setStatus("[data-parameter-status]", error.message || "Could not expand the parameters.");
        } finally {
            trigger.disabled = false;
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

    async function loadTextFile(input, target) {
        const file = input.files && input.files[0];
        if (!file) return;
        if (file.size > 2 * 1024 * 1024) { notify("Files must be 2 MB or smaller.", "error"); input.value = ""; return; }
        const text = await file.text();
        if (!target) return;
        const parsed = file.name.toLowerCase().endsWith(".csv")
            ? lines(text).map((line) => line.split(",")[0].replace(/^"|"$/g, ""))
            : lines(text);
        target.value = [target.value.trim(), parsed.join("\n")].filter(Boolean).join("\n");
        updatePreview();
    }

    async function insertKeywordSet(trigger) {
        const picker = wizard.querySelector("[data-keyword-set-picker]");
        if (!picker || !picker.value) { notify("Choose a saved keyword set first.", "error"); return; }
        trigger.disabled = true;
        try {
            const response = await fetch("/api/v1/keyword-sets/" + encodeURIComponent(picker.value) + "/use", {
                method: "POST",
                credentials: "same-origin",
                headers: { "Accept": "application/json", "X-CSRF-Token": value("csrf_token", "") }
            });
            const payload = await response.json().catch(() => ({}));
            if (!response.ok) throw new Error((payload.error && payload.error.message) || "Could not load the keyword set");
            const keywords = payload.data && Array.isArray(payload.data.keywords) ? payload.data.keywords : [];
            if (!keywords.length) throw new Error("The keyword set has no keywords");
            const result = appendUniqueLines(keywords);
            notify(result.added + " keywords inserted" + (result.skipped ? "; " + result.skipped + " already present" : "") + ".", "success");
        } catch (error) {
            notify(error.message || "Could not load the keyword set.", "error");
        } finally {
            trigger.disabled = false;
        }
    }

    async function saveKeywordSet(trigger) {
        const nameInput = wizard.querySelector("[data-keyword-set-name]");
        const name = nameInput ? nameInput.value.trim() : "";
        if (!name) { setStatus("[data-keyword-set-status]", "Name the set before saving it."); return; }
        const keywords = uniqueQueries().unique;
        if (!keywords.length) { setStatus("[data-keyword-set-status]", "Add at least one query line first."); return; }
        trigger.disabled = true;
        try {
            const response = await fetch("/api/v1/keyword-sets", {
                method: "POST",
                credentials: "same-origin",
                headers: { "Accept": "application/json", "Content-Type": "application/json", "X-CSRF-Token": value("csrf_token", "") },
                body: JSON.stringify({ name: name, keywords: keywords })
            });
            const payload = await response.json().catch(() => ({}));
            if (!response.ok) throw new Error((payload.error && payload.error.message) || "Could not save the keyword set");
            const saved = payload.data || {};
            const picker = wizard.querySelector("[data-keyword-set-picker]");
            if (picker && saved.id) {
                let option = picker.querySelector('option[value="' + saved.id + '"]');
                if (!option) { option = document.createElement("option"); option.value = saved.id; picker.appendChild(option); }
                option.textContent = saved.name + " (" + keywords.length + " keywords, used " + (saved.use_count || 0) + "×)";
            }
            setStatus("[data-keyword-set-status]", "Saved “" + (saved.name || name) + "” with " + keywords.length + " keywords.");
            notify("Keyword set saved.", "success");
        } catch (error) {
            setStatus("[data-keyword-set-status]", error.message || "Could not save the keyword set.");
            notify(error.message || "Could not save the keyword set.", "error");
        } finally {
            trigger.disabled = false;
        }
    }

    function commaTerms(selector) {
        const input = wizard.querySelector(selector);
        return input ? input.value.split(",").map((term) => term.trim().toLocaleLowerCase()).filter(Boolean) : [];
    }

    // applyKeywordFilters rewrites the queries textarea on an explicit click:
    // a line survives when it contains at least one include term (if any are
    // given) and none of the exclude terms. The preview count follows.
    function applyKeywordFilters() {
        const target = field("keywords");
        if (!target) return;
        const include = commaTerms("[data-include-terms]");
        const exclude = commaTerms("[data-exclude-terms]");
        const before = lines(target.value);
        const kept = before.filter((line) => {
            const lowered = line.toLocaleLowerCase();
            if (include.length && !include.some((term) => lowered.includes(term))) return false;
            return !exclude.some((term) => lowered.includes(term));
        });
        target.value = kept.join("\n");
        updatePreview();
        setStatus("[data-keyword-filter-status]", "Kept " + kept.length + " of " + before.length + " lines.");
    }

    function generateCombinations() {
        const categoriesInput = wizard.querySelector("[data-combo-categories]");
        const locationsInput = wizard.querySelector("[data-combo-locations]");
        const categories = lines(categoriesInput && categoriesInput.value);
        const comboLocations = lines(locationsInput && locationsInput.value);
        if (!categories.length || !comboLocations.length) {
            setStatus("[data-combo-status]", "Add at least one category and one location.");
            return;
        }
        const combinations = [];
        categories.forEach((category) => comboLocations.forEach((location) => combinations.push(category + " in " + location)));
        const result = appendUniqueLines(combinations);
        setStatus("[data-combo-status]", "Generated " + combinations.length + " combinations; added " + result.added +
            " new queries" + (result.skipped ? ", skipped " + result.skipped + " duplicates" : "") + ".");
    }

    // applyProspectingPipelinePreset switches on the enrichment-step fields
    // that complete the standalone GBP pipeline (coverage -> scrape -> dedupe
    // -> website pre-classification -> email discovery -> scoring -> call
    // openers). It only runs when GBP coverage queries are generated with the
    // preset checkbox ticked, so defaults for other scrapes never change.
    function applyProspectingPipelinePreset() {
        const preset = wizard.querySelector("[data-gbp-pipeline]");
        if (!preset || !preset.checked) return false;
        const email = field("email");
        const checkMX = field("enrichment_check_mx");
        const scope = field("enrichment_scope");
        if (email) email.checked = true;
        if (checkMX) checkMX.checked = true;
        if (scope) scope.value = "homepage_contact_about";
        updatePreview();
        return true;
    }

    // generateGBPQueries asks the local prospecting API for ZIP x synonym
    // coverage queries and merges them into the keywords list. The returned
    // centre only replaces the map centre while it is still empty or at the
    // San Francisco example default, so a deliberate location is never lost.
    async function generateGBPQueries(trigger) {
        const stateInput = wizard.querySelector("[data-gbp-state]");
        const cityInput = wizard.querySelector("[data-gbp-city]");
        const topNInput = wizard.querySelector("[data-gbp-top-n]");
        const synonymsInput = wizard.querySelector("[data-gbp-synonyms]");
        const zipsInput = wizard.querySelector("[data-gbp-zips-file]");
        const state = stateInput ? stateInput.value.trim() : "";
        const synonyms = lines(synonymsInput && synonymsInput.value);
        if (state.length !== 2) { setStatus("[data-gbp-status]", "Enter the 2-letter state code first."); return; }
        if (!synonyms.length) { setStatus("[data-gbp-status]", "Add at least one category synonym."); return; }
        const body = new FormData();
        body.set("state", state);
        body.set("city", cityInput ? cityInput.value.trim() : "");
        body.set("top_n", topNInput && topNInput.value ? topNInput.value : "25");
        body.set("synonyms", synonyms.join("\n"));
        const zipFile = zipsInput && zipsInput.files && zipsInput.files[0];
        if (zipFile) {
            if (zipFile.size > 2 * 1024 * 1024) { setStatus("[data-gbp-status]", "The ZIP CSV must be 2 MB or smaller."); return; }
            body.set("zips_csv", zipFile);
        }
        trigger.disabled = true;
        setStatus("[data-gbp-status]", "Generating coverage queries…");
        try {
            const response = await fetch("/api/v1/prospects/queries", {
                method: "POST",
                credentials: "same-origin",
                headers: { "Accept": "application/json", "X-CSRF-Token": value("csrf_token", "") },
                body: body
            });
            const payload = await response.json().catch(() => ({}));
            if (!response.ok) throw new Error((payload.error && payload.error.message) || "Could not generate coverage queries");
            const data = payload.data || {};
            const queries = Array.isArray(data.queries) ? data.queries : [];
            if (!queries.length) throw new Error("No queries were generated for that state and city");
            const result = appendUniqueLines(queries);
            const centre = Array.isArray(data.centre) ? data.centre : [];
            if (centre.length === 2 && Number.isFinite(Number(centre[0])) && Number.isFinite(Number(centre[1]))) {
                const latitudeField = field("latitude");
                const longitudeField = field("longitude");
                const sfDefault = (latitudeField && (latitudeField.value === "" || latitudeField.value === "37.7749")) &&
                    (longitudeField && (longitudeField.value === "" || longitudeField.value === "-122.4194"));
                if (sfDefault) {
                    latitudeField.value = String(centre[0]);
                    longitudeField.value = String(centre[1]);
                    updatePreview();
                }
            }
            const zipCount = Number(data.zip_count) || 0;
            const pipelineApplied = applyProspectingPipelinePreset();
            setStatus("[data-gbp-status]", queries.length + " queries across " + zipCount + " ZIPs; added " + result.added +
                " new" + (result.skipped ? ", skipped " + result.skipped + " duplicates" : "") +
                (pipelineApplied ? ". Prospecting pipeline enrichment enabled (step 4)." : "."));
            notify("GBP coverage queries added.", "success");
        } catch (error) {
            setStatus("[data-gbp-status]", error.message || "Could not generate coverage queries.");
            notify(error.message || "Could not generate coverage queries.", "error");
        } finally {
            trigger.disabled = false;
        }
    }

    // --- Campaign templates (optional server capability) --------------------
    // A reusable-template list endpoint may or may not exist on this build. The
    // picker is rendered hidden and only revealed once a well-formed JSON list
    // actually comes back, so a repository without the capability shows no dead
    // control instead of a button that fails on click. The "Rerun as campaign"
    // action needs a rerun URL on the template itself and stays hidden without
    // one for the same reason.
    let campaignTemplates = [];

    function campaignRerunURL(template) {
        if (!template) return "";
        const candidate = template.rerun_url || template.rescan_url || template.campaign_url || "";
        return typeof candidate === "string" && candidate.startsWith("/") ? candidate : "";
    }

    async function probeCampaignTemplates() {
        const host = wizard.querySelector("[data-campaign-templates]");
        const picker = wizard.querySelector("[data-campaign-template-picker]");
        if (!host || !picker) return;
        try {
            const response = await fetch("/api/v1/templates", {
                credentials: "same-origin",
                headers: { "Accept": "application/json" }
            });
            if (!response.ok) return;
            const payload = await response.json();
            const items = payload && Array.isArray(payload.data) ? payload.data : [];
            campaignTemplates = items.filter((item) => item && item.id && item.name);
            if (!campaignTemplates.length) return;
            campaignTemplates.forEach((template) => {
                const option = document.createElement("option");
                option.value = template.id;
                option.textContent = template.name;
                picker.appendChild(option);
            });
            host.hidden = false;
        } catch (_) {
            // No template list on this build: the control simply stays hidden.
        }
    }

    function selectedCampaignTemplate() {
        const picker = wizard.querySelector("[data-campaign-template-picker]");
        if (!picker || !picker.value) return null;
        return campaignTemplates.find((template) => template.id === picker.value) || null;
    }

    function syncCampaignRerun() {
        const rerun = wizard.querySelector("[data-campaign-rerun]");
        if (!rerun) return;
        rerun.hidden = !campaignRerunURL(selectedCampaignTemplate());
    }

    function applyCampaignTemplate() {
        const template = selectedCampaignTemplate();
        if (!template) { notify("Choose a template first.", "error"); return; }
        window.location.assign("/app/scrapes/new?template=" + encodeURIComponent(template.id));
    }

    async function rerunCampaign(trigger) {
        const template = selectedCampaignTemplate();
        const url = campaignRerunURL(template);
        if (!url) return;
        trigger.disabled = true;
        try {
            const response = await fetch(url, {
                method: "POST",
                credentials: "same-origin",
                headers: { "Accept": "application/json", "X-CSRF-Token": value("csrf_token", "") }
            });
            const payload = await response.json().catch(() => ({}));
            if (!response.ok) throw new Error((payload.error && payload.error.message) || "Could not rerun the campaign");
            notify("Campaign rerun queued.", "success");
            const target = payload.data && payload.data.url;
            if (typeof target === "string" && target.startsWith("/")) window.location.assign(target);
        } catch (error) {
            notify(error.message || "Could not rerun the campaign.", "error");
        } finally {
            trigger.disabled = false;
        }
    }

    async function generateLocalAIKeywords(trigger) {
        const input = wizard.querySelector("[data-local-ai-input]");
        const status = wizard.querySelector("[data-local-ai-status]");
        if (!input || !input.value.trim()) {
            if (window.GMapsApp) window.GMapsApp.toast("Describe the businesses and location first.", "error");
            return;
        }
        trigger.disabled = true;
        if (status) status.textContent = "Waiting for the local modelâ€¦";
        try {
            const response = await fetch("/api/v1/ai/assist", {
                method: "POST",
                credentials: "same-origin",
                headers: { "Accept": "application/json", "Content-Type": "application/json", "X-CSRF-Token": value("csrf_token", "") },
                body: JSON.stringify({ task: "keyword_variations", input: input.value.trim(), context: { existing_queries: uniqueQueries().unique } })
            });
            const payload = await response.json().catch(() => ({}));
            if (!response.ok) throw new Error((payload.error && payload.error.message) || "Local AI request failed");
            const result = payload.data && payload.data.result;
            const suggestions = result && Array.isArray(result.keywords) ? result.keywords.filter((item) => typeof item === "string" && item.trim()).slice(0, 30) : [];
            if (!suggestions.length) throw new Error("The local model returned no usable keywords");
            const target = field("keywords");
            target.value = [target.value.trim(), suggestions.join("\n")].filter(Boolean).join("\n");
            updatePreview();
            if (status) status.textContent = suggestions.length + " suggestions added; review them before launch.";
            if (window.GMapsApp) window.GMapsApp.toast("Local keyword suggestions added.", "success");
        } catch (error) {
            if (status) status.textContent = error.message || "Local AI request failed.";
            if (window.GMapsApp) window.GMapsApp.toast(error.message || "Local AI request failed.", "error");
        } finally {
            trigger.disabled = false;
        }
    }

    wizard.addEventListener("click", (event) => {
        const chip = event.target.closest("[data-category-name]");
        if (chip) { event.preventDefault(); toggleCategoryChip(chip); return; }
        const trigger = event.target.closest("[data-action]");
        if (!trigger) return;
        if (trigger.dataset.action === "wizard-step") { event.preventDefault(); setStep(trigger.dataset.stepTarget, true); }
        else if (trigger.dataset.action === "wizard-next") { event.preventDefault(); setStep(current + 1, true); }
        else if (trigger.dataset.action === "wizard-back") { event.preventDefault(); setStep(current - 1, true); }
        else if (trigger.dataset.action === "use-san-francisco-preset") { event.preventDefault(); applySanFranciscoPreset(); }
        else if (trigger.dataset.action === "preview-queries") { event.preventDefault(); updatePreview(); }
        else if (trigger.dataset.action === "local-ai-keywords") { event.preventDefault(); generateLocalAIKeywords(trigger); }
        else if (trigger.dataset.action === "insert-keyword-set") { event.preventDefault(); insertKeywordSet(trigger); }
        else if (trigger.dataset.action === "save-keyword-set") { event.preventDefault(); saveKeywordSet(trigger); }
        else if (trigger.dataset.action === "apply-keyword-filters") { event.preventDefault(); applyKeywordFilters(); }
        else if (trigger.dataset.action === "generate-combinations") { event.preventDefault(); generateCombinations(); }
        else if (trigger.dataset.action === "generate-gbp-queries") { event.preventDefault(); generateGBPQueries(trigger); }
        else if (trigger.dataset.action === "apply-campaign-template") { event.preventDefault(); applyCampaignTemplate(); }
        else if (trigger.dataset.action === "rerun-campaign") { event.preventDefault(); rerunCampaign(trigger); }
        else if (trigger.dataset.action === "select-all-fields") { event.preventDefault(); setAllFields(null); }
        else if (trigger.dataset.action === "select-core-fields") { event.preventDefault(); setAllFields(CORE_FIELD_KEYS); }
        else if (trigger.dataset.action === "insert-categories") { event.preventDefault(); insertSelectedCategories(); }
        else if (trigger.dataset.action === "clear-categories") { event.preventDefault(); clearSelectedCategories(); }
        else if (trigger.dataset.action === "insert-category-group") { event.preventDefault(); insertCategoryGroup(); }
        else if (trigger.dataset.action === "save-category-group") { event.preventDefault(); saveCategoryGroup(trigger); }
        else if (trigger.dataset.action === "delete-category-group") { event.preventDefault(); deleteCategoryGroup(trigger); }
        else if (trigger.dataset.action === "preview-parameters") { event.preventDefault(); previewParameters(trigger); }
    });

    wizard.addEventListener("change", (event) => {
        if (event.target.matches("[data-wizard-mode-input]")) {
            applyMode(event.target.value, true);
            return;
        }
        if (event.target.matches("[data-campaign-template-picker]")) {
            syncCampaignRerun();
            return;
        }
        if (event.target.matches("[data-saved-area-picker]")) {
            const areaID = event.target.value;
            if (areaID) window.location.assign("/app/scrapes/new?area_id=" + encodeURIComponent(areaID));
            return;
        }
        if (event.target.matches("[data-fields-toggle]") || event.target.name === "fields") {
            syncFieldSelection();
            return;
        }
        if (event.target.matches("[data-category-sector]")) { renderCategoryOptions(); return; }
        if (event.target.name === "incremental_mode") { syncIncrementalNote(); return; }
        if (event.target.matches("[data-keywords-file]")) loadTextFile(event.target, field("keywords"));
        else if (event.target.matches("[data-locations-file]")) loadTextFile(event.target, field("locations"));
        else if (event.target.matches("[data-combo-locations-file]")) loadTextFile(event.target, wizard.querySelector("[data-combo-locations]"));
        else if (event.target.name === "performance_preset") applyPerformancePreset(event.target.value);
        else updatePreview();
    });
    wizard.addEventListener("input", (event) => {
        if (event.target.matches("[data-category-search]")) { renderCategoryOptions(); return; }
        if (["keywords", "locations", "radius", "grid_cell_km", "depth", "concurrency", "browser_pool_size", "pages_per_browser", "maxtime"].includes(event.target.name)) updatePreview();
    });

    // A required control inside a step the current mode hides cannot be
    // focused, which would make submit fail silently. Reveal its step (and
    // leave Basic mode if that is what is hiding it) before the browser
    // reports the problem.
    if (form) {
        form.addEventListener("invalid", (event) => {
            const panel = event.target.closest && event.target.closest("[data-wizard-panel]");
            if (!panel) return;
            if (panel.dataset.modeHidden === "true") applyMode("advanced", true);
            setStep(panel.dataset.wizardPanel, false);
        }, true);
    }

    panels.forEach((panel, index) => { panel.hidden = index !== 0; });
    applyMode(requestedMode() || readStoredMode() || "advanced", false);
    setStep(requestedStep() || 1, false);
    syncFieldSelection();
    syncIncrementalNote();
    updatePreview();
    probeCampaignTemplates();
    loadCategoryCatalogue();
})();
