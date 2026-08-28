(function () {
    "use strict";

    // The export builder's one job that cannot be ambiguous is saying which
    // businesses a file will contain. This script makes the chosen scope the
    // only thing that decides that:
    //
    //   * it shows the live count of every scope, taken from the same query
    //     the export itself runs, before the file is built;
    //   * it disables and clears the inputs the chosen scope does not use, so
    //     a filter left on screen can never be quietly ignored;
    //   * it names the submit button after the scope and its count.
    //
    // Everything degrades to the plain form when the count request fails.

    const form = document.querySelector("[data-export-scope-form]");
    if (!form) return;

    const scopeSelect = form.querySelector('select[name="source_scope"]');
    const summary = form.querySelector("[data-scope-summary]");
    const counts = form.querySelector("[data-scope-counts]");
    const owningForm = form.closest("form");
    const submit = owningForm && owningForm.querySelector("[data-scope-submit]");
    if (!scopeSelect || !owningForm) return;

    // Which named inputs each scope actually consumes. This mirrors
    // exportScopeInputs in web/advanced_filters.go; the server refuses a
    // request that carries anything else, so the two must agree.
    const scopeInputs = {
        filtered: ["q", "filters", "job_id"],
        selected: ["selected_ids"],
        job: ["job_id"],
        all: [],
        saved_view: ["saved_view_id"]
    };

    const scopeLabels = {
        filtered: "Current filtered view",
        selected: "Selected businesses",
        job: "Current source job",
        all: "Entire workspace",
        saved_view: "Saved view"
    };

    let latestScopes = [];

    function scopeGroups() {
        return Array.from(form.querySelectorAll("[data-scope-input]"))
            .concat(Array.from(owningForm.querySelectorAll("[data-scope-input]")));
    }

    function controlsIn(group) {
        return Array.from(group.querySelectorAll("input, select, textarea"));
    }

    // A scope that does not use an input clears it and turns it off, so the
    // browser never submits it. Clearing is what keeps the server's
    // "this scope ignores X" refusal from ever being reached by accident.
    function applyScopeInputs(scope) {
        const used = scopeInputs[scope] || [];
        const seen = new Set();
        scopeGroups().forEach((group) => {
            const name = group.dataset.scopeInput;
            if (seen.has(group)) return;
            seen.add(group);
            const active = used.indexOf(name) !== -1;
            group.hidden = !active;
            controlsIn(group).forEach((control) => {
                control.disabled = !active;
                if (active) return;
                if (control.type === "checkbox" || control.type === "radio") {
                    control.checked = false;
                } else if (control.tagName === "SELECT") {
                    control.selectedIndex = 0;
                } else {
                    control.value = "";
                }
            });
        });
    }

    function scopeEntry(scope) {
        return latestScopes.filter(function (entry) { return entry && entry.key === scope; })[0] || null;
    }

    function describeScope(scope) {
        const entry = scopeEntry(scope);
        const label = scopeLabels[scope] || scope;
        if (!entry) return label;
        if (entry.reason) {
            return label + ": " + entry.count + " businesses — " + entry.reason;
        }
        return label + ": " + entry.count + " businesses";
    }

    function renderSummary() {
        const scope = scopeSelect.value;
        const entry = scopeEntry(scope);
        if (summary) {
            if (!entry) {
                summary.hidden = true;
            } else {
                summary.hidden = false;
                summary.textContent = "This export will contain " + entry.count +
                    " businesses — " + (scopeLabels[scope] || scope) +
                    (entry.reason ? " — " + entry.reason : "") + ".";
            }
        }
        // A scope the server would refuse is refused here first, with the
        // reason on the button, so the operator never learns about it from a
        // failed submission.
        if (submit) {
            if (!entry) {
                submit.textContent = "Create export";
                submit.disabled = false;
                submit.removeAttribute("title");
            } else if (entry.available === false) {
                submit.textContent = "Create export — " + (scopeLabels[scope] || scope);
                submit.disabled = true;
                submit.title = entry.reason || "";
            } else {
                submit.textContent = "Create export — " + (scopeLabels[scope] || scope) +
                    " (" + entry.count + ")";
                submit.disabled = false;
                submit.removeAttribute("title");
            }
        }
    }

    function renderCounts() {
        if (!counts) return;
        if (!latestScopes.length) {
            counts.hidden = true;
            return;
        }
        counts.hidden = false;
        counts.replaceChildren();
        latestScopes.forEach(function (entry) {
            if (!entry || !scopeLabels[entry.key]) return;
            const item = document.createElement("span");
            item.className = "chip chip-static";
            if (entry.key === scopeSelect.value) item.dataset.selected = "true";
            const name = document.createElement("span");
            name.textContent = scopeLabels[entry.key];
            const value = document.createElement("span");
            value.className = "chip-count";
            value.textContent = String(entry.count);
            item.appendChild(name);
            item.appendChild(value);
            if (entry.reason) item.title = entry.reason;
            counts.appendChild(item);
        });
    }

    // The counts are asked for with exactly the inputs currently on the form,
    // so they answer "how many would leave if I pressed the button now?".
    function currentScopeQuery() {
        const endpoint = form.dataset.scopeEndpoint || "/api/v1/exports/scopes";
        const target = new URL(endpoint, window.location.origin);
        const data = new FormData(owningForm);
        const carried = [
            "q", "job_id", "saved_view_id", "selected_ids",
            "filter_field", "filter_operator", "filter_value", "filter_json",
            "include_duplicates", "sort"
        ];
        carried.forEach(function (name) {
            data.getAll(name).forEach(function (value) {
                if (typeof value === "string" && value !== "") target.searchParams.append(name, value);
            });
        });
        target.searchParams.set("page_size", "1");
        return target.toString();
    }

    let pending = null;

    function loadCounts() {
        const url = currentScopeQuery();
        if (pending === url) return;
        pending = url;
        window.fetch(url, { headers: { Accept: "application/json" } })
            .then(function (response) { return response.ok ? response.json() : null; })
            .then(function (payload) {
                if (!payload || !Array.isArray(payload.data)) return;
                latestScopes = payload.data;
                renderCounts();
                renderSummary();
            })
            .catch(function () { /* the form still works without the preview */ });
    }

    // A deep link from Results carries ?scope=<key>; the page itself only
    // understands source_scope, so the link stays valid either way.
    function restoreRequestedScope() {
        const requested = new URL(window.location.href).searchParams.get("scope");
        if (!requested) return;
        const options = Array.from(scopeSelect.options).map(function (option) { return option.value; });
        if (options.indexOf(requested) !== -1) scopeSelect.value = requested;
    }

    scopeSelect.addEventListener("change", function () {
        applyScopeInputs(scopeSelect.value);
        renderSummary();
        loadCounts();
    });
    owningForm.addEventListener("change", function (event) {
        if (event.target === scopeSelect) return;
        const name = event.target && event.target.name;
        if (name === "job_id" || name === "saved_view_id" || name === "q") loadCounts();
    });

    restoreRequestedScope();
    applyScopeInputs(scopeSelect.value);
    loadCounts();
})();
