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
            setStatus("[data-gbp-status]", queries.length + " queries across " + zipCount + " ZIPs; added " + result.added +
                " new" + (result.skipped ? ", skipped " + result.skipped + " duplicates" : "") + ".");
            notify("GBP coverage queries added.", "success");
        } catch (error) {
            setStatus("[data-gbp-status]", error.message || "Could not generate coverage queries.");
            notify(error.message || "Could not generate coverage queries.", "error");
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
    });

    wizard.addEventListener("change", (event) => {
        if (event.target.matches("[data-saved-area-picker]")) {
            const areaID = event.target.value;
            if (areaID) window.location.assign("/app/scrapes/new?area_id=" + encodeURIComponent(areaID));
            return;
        }
        if (event.target.matches("[data-keywords-file]")) loadTextFile(event.target, field("keywords"));
        else if (event.target.matches("[data-locations-file]")) loadTextFile(event.target, field("locations"));
        else if (event.target.matches("[data-combo-locations-file]")) loadTextFile(event.target, wizard.querySelector("[data-combo-locations]"));
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
