(function () {
    "use strict";

    // Prospecting settings editors: three small JSON-backed forms (score
    // weights, call openers, integration endpoints) rendered from GET
    // responses and saved with JSON PUTs. The module is dependency-free and
    // CSP-safe: it lives in this external file and injects no inline code.
    const container = document.querySelector("[data-prospect-settings]");
    if (!container) return;

    function csrfToken() {
        const input = document.querySelector('input[name="csrf_token"]');
        return input ? input.value : "";
    }

    function labelFor(key) {
        const label = String(key).replace(/_/g, " ").trim();
        return label.charAt(0).toUpperCase() + label.slice(1);
    }

    function setStatus(editor, message) {
        const status = editor.querySelector("[data-prospect-status]");
        if (status) status.textContent = message;
    }

    // renderFields builds one labelled control per primitive field in the
    // fetched object. Nested objects and arrays are left untouched so a
    // future API change cannot silently drop data on save.
    function renderFields(editor, kind, data) {
        const host = editor.querySelector("[data-prospect-fields]");
        if (!host) return 0;
        host.replaceChildren();
        let rendered = 0;
        Object.keys(data).sort((a, b) => (a === "default") - (b === "default") || a.localeCompare(b)).forEach((key) => {
            const value = data[key];
            const type = typeof value;
            if (type !== "number" && type !== "string" && type !== "boolean") return;
            const fieldWrap = document.createElement("div");
            fieldWrap.className = "field";
            const id = "prospect-" + kind + "-" + key.replace(/[^a-z0-9_-]/gi, "");
            const label = document.createElement("label");
            label.htmlFor = id;
            label.textContent = labelFor(key);
            fieldWrap.appendChild(label);
            let control;
            if (kind === "openers" || (type === "string" && value.length > 80)) {
                control = document.createElement("textarea");
                control.className = "textarea";
                control.rows = 2;
                control.value = String(value);
            } else if (type === "boolean") {
                control = document.createElement("input");
                control.className = "checkbox";
                control.type = "checkbox";
                control.checked = value;
            } else {
                control = document.createElement("input");
                control.className = "input";
                control.type = type === "number" ? "number" : "text";
                if (type === "number") control.step = "any";
                control.value = String(value);
            }
            control.id = id;
            control.dataset.prospectField = key;
            control.dataset.prospectType = type;
            fieldWrap.appendChild(control);
            host.appendChild(fieldWrap);
            rendered += 1;
        });
        return rendered;
    }

    function collectFields(editor) {
        const body = {};
        editor.querySelectorAll("[data-prospect-field]").forEach((control) => {
            const key = control.dataset.prospectField;
            const type = control.dataset.prospectType;
            if (type === "number") {
                const parsed = Number(control.value);
                body[key] = Number.isFinite(parsed) ? parsed : 0;
            } else if (type === "boolean") {
                body[key] = Boolean(control.checked);
            } else {
                body[key] = control.value;
            }
        });
        return body;
    }

    async function loadEditor(editor) {
        const kind = editor.dataset.prospectEditor;
        const endpoint = editor.dataset.endpoint;
        try {
            const response = await fetch(endpoint, {
                credentials: "same-origin",
                headers: { "Accept": "application/json" }
            });
            const payload = await response.json().catch(() => ({}));
            if (!response.ok) throw new Error((payload.error && payload.error.message) || "not available");
            const data = payload.data && typeof payload.data === "object" ? payload.data : {};
            const rendered = renderFields(editor, kind, data);
            if (!rendered) {
                setStatus(editor, "The prospecting API returned no editable fields.");
                return;
            }
            const save = editor.querySelector("[data-action=save-prospect-editor]");
            if (save) save.hidden = false;
            setStatus(editor, "");
        } catch (error) {
            setStatus(editor, "Unavailable: " + (error.message || "the prospecting API is not enabled in this build."));
        }
    }

    async function saveEditor(editor, trigger) {
        const endpoint = editor.dataset.endpoint;
        trigger.disabled = true;
        setStatus(editor, "Saving…");
        try {
            const response = await fetch(endpoint, {
                method: "PUT",
                credentials: "same-origin",
                headers: {
                    "Accept": "application/json",
                    "Content-Type": "application/json",
                    "X-CSRF-Token": csrfToken()
                },
                body: JSON.stringify(collectFields(editor))
            });
            const payload = await response.json().catch(() => ({}));
            if (!response.ok) throw new Error((payload.error && payload.error.message) || "could not save");
            setStatus(editor, "Saved.");
            if (window.GMapsApp) window.GMapsApp.toast("Prospecting settings saved.", "success");
        } catch (error) {
            setStatus(editor, "Save failed: " + (error.message || "unknown error."));
            if (window.GMapsApp) window.GMapsApp.toast(error.message || "Could not save prospecting settings.", "error");
        } finally {
            trigger.disabled = false;
        }
    }

    container.addEventListener("click", (event) => {
        const trigger = event.target.closest("[data-action=save-prospect-editor]");
        if (!trigger) return;
        event.preventDefault();
        const editor = trigger.closest("[data-prospect-editor]");
        if (editor) saveEditor(editor, trigger);
    });

    container.querySelectorAll("[data-prospect-editor]").forEach(loadEditor);
})();
