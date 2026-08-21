(function () {
    "use strict";

    const workspace = document.querySelector("[data-api-workspace]");
    if (!workspace) return;
    const csrf = workspace.dataset.csrfToken || "";

    // The quick-start card shows one language at a time. Every panel is in the
    // document, so the page still works — and stays copyable — without script.
    const exampleBlock = workspace.querySelector("[data-example-block]");
    if (exampleBlock) exampleBlock.addEventListener("click", (event) => {
        const tab = event.target.closest("[data-example-tab]");
        if (!tab) return;
        const language = tab.dataset.exampleTab;
        exampleBlock.querySelectorAll("[data-example-tab]").forEach((option) => {
            option.setAttribute("aria-selected", option.dataset.exampleTab === language ? "true" : "false");
        });
        exampleBlock.querySelectorAll("[data-example-panel]").forEach((panel) => {
            panel.hidden = panel.dataset.examplePanel !== language;
        });
    });

    // Filtering the generated reference is a local text match over the method,
    // path, and purpose of each row; groups with no visible row are hidden so
    // the page never shows an empty heading.
    const reference = workspace.querySelector("[data-reference]");
    const referenceFilter = workspace.querySelector("[data-reference-filter]");
    if (reference && referenceFilter) referenceFilter.addEventListener("input", () => {
        const needle = referenceFilter.value.trim().toLowerCase();
        let visible = 0;
        reference.querySelectorAll("[data-reference-group]").forEach((group) => {
            let shown = 0;
            group.querySelectorAll("[data-reference-row]").forEach((row) => {
                const match = !needle || (row.dataset.referenceText || "").toLowerCase().includes(needle);
                row.hidden = !match;
                if (match) shown += 1;
            });
            group.hidden = shown === 0;
            visible += shown;
        });
        const empty = reference.querySelector("[data-reference-empty]");
        if (empty) empty.hidden = visible !== 0;
    });

    async function request(endpoint, options) {
        const settings = Object.assign({ credentials: "same-origin", headers: {} }, options || {});
        settings.headers = Object.assign({ Accept: "application/json", "X-CSRF-Token": csrf }, settings.headers || {});
        const response = await fetch(endpoint, settings);
        const payload = await response.json().catch(() => ({}));
        if (!response.ok) throw new Error((payload.error && payload.error.message) || payload.message || "Request failed");
        return payload.data || payload;
    }

    function report(error) {
        if (window.GMapsApp) window.GMapsApp.toast(error.message || String(error), "error");
    }

    // withBusy gives every async control the same in-flight appearance: the
    // trigger is disabled and marked busy until the request settles, so a slow
    // local database cannot look like an inert button.
    async function withBusy(control, work) {
        if (control) {
            control.setAttribute("aria-busy", "true");
            control.disabled = true;
        }
        try {
            return await work();
        } finally {
            if (control) {
                control.removeAttribute("aria-busy");
                control.disabled = false;
            }
        }
    }

    function submitControl(form, event) {
        return (event && event.submitter) || form.querySelector('[type="submit"]');
    }

    const keyForm = workspace.querySelector("[data-api-key-form]");
    if (keyForm) keyForm.addEventListener("submit", async (event) => {
        event.preventDefault();
        try {
            const data = await withBusy(submitControl(keyForm, event), () => request(keyForm.action, { method: "POST", body: new FormData(keyForm) }));
            const target = workspace.querySelector("[data-api-key-token]");
            target.hidden = false;
            target.className = "notice notice-warning";
            target.replaceChildren();
            const body = document.createElement("div");
            const title = document.createElement("strong");
            title.textContent = "Copy this key now; it cannot be shown again.";
            const token = document.createElement("pre");
            token.className = "code-block";
            token.textContent = data.token || "";
            body.append(title, token);
            target.appendChild(body);
            if (window.GMapsApp) window.GMapsApp.toast("API key created.", "success");
        } catch (error) { report(error); }
    });

    const rateForm = workspace.querySelector("[data-api-rate-form]");
    if (rateForm) rateForm.addEventListener("submit", async (event) => {
        event.preventDefault();
        try {
            await withBusy(submitControl(rateForm, event), () => request("/api/v1/api/settings", {
                method: "PUT", headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ rate_limit_per_minute: Number(new FormData(rateForm).get("rate_limit_per_minute")) })
            }));
            if (window.GMapsApp) window.GMapsApp.toast("API rate limit saved.", "success");
        } catch (error) { report(error); }
    });

    const integrationForm = workspace.querySelector("[data-integration-form]");

    // Only the fields that belong to the selected destination stay visible, so
    // a webhook never shows a database DSN and the form cannot be submitted
    // with contradictory configuration.
    function syncIntegrationKind() {
        if (!integrationForm) return;
        const picker = integrationForm.querySelector("[data-integration-kind]");
        const kind = picker ? picker.value : "webhook";
        integrationForm.querySelectorAll("[data-integration-field]").forEach((group) => {
            group.hidden = group.dataset.integrationField !== kind;
        });
    }

    if (integrationForm) {
        syncIntegrationKind();
        integrationForm.addEventListener("change", (event) => {
            if (event.target.matches("[data-integration-kind]")) syncIntegrationKind();
        });
        integrationForm.addEventListener("submit", async (event) => {
            event.preventDefault();
            try {
                await withBusy(submitControl(integrationForm, event), () => request(integrationForm.action, { method: "POST", body: new FormData(integrationForm) }));
                window.location.reload();
            } catch (error) { report(error); }
        });
    }

    workspace.addEventListener("click", async (event) => {
        const key = event.target.closest("[data-api-key-toggle]");
        const remove = event.target.closest("[data-integration-delete]");
        const test = event.target.closest("[data-integration-test]");
        try {
            if (key) {
                await withBusy(key, () => request("/api/v1/api-keys/" + encodeURIComponent(key.dataset.keyId) + "/" + key.dataset.apiKeyToggle, { method: "POST" }));
                window.location.reload();
            } else if (test) {
                await withBusy(test, () => request("/api/v1/integrations/" + encodeURIComponent(test.dataset.integrationId) + "/test", { method: "POST" }));
                if (window.GMapsApp) window.GMapsApp.toast("Signed test delivery sent.", "success");
                window.location.reload();
            } else if (remove && window.confirm("Delete this local integration? Earlier delivery history is removed with it.")) {
                await withBusy(remove, () => request("/api/v1/integrations/" + encodeURIComponent(remove.dataset.integrationId), { method: "DELETE" }));
                window.location.reload();
            }
        } catch (error) { report(error); }
    });
}());
