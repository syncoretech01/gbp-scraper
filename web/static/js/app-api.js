(function () {
    "use strict";

    const workspace = document.querySelector("[data-api-workspace]");
    if (!workspace) return;
    const csrf = workspace.dataset.csrfToken || "";

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

    const keyForm = workspace.querySelector("[data-api-key-form]");
    if (keyForm) keyForm.addEventListener("submit", async (event) => {
        event.preventDefault();
        try {
            const data = await request(keyForm.action, { method: "POST", body: new FormData(keyForm) });
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
            await request("/api/v1/api/settings", {
                method: "PUT", headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ rate_limit_per_minute: Number(new FormData(rateForm).get("rate_limit_per_minute")) })
            });
            if (window.GMapsApp) window.GMapsApp.toast("API rate limit saved.", "success");
        } catch (error) { report(error); }
    });

    const integrationForm = workspace.querySelector("[data-integration-form]");
    if (integrationForm) integrationForm.addEventListener("submit", async (event) => {
        event.preventDefault();
        try {
            await request(integrationForm.action, { method: "POST", body: new FormData(integrationForm) });
            window.location.reload();
        } catch (error) { report(error); }
    });

    workspace.addEventListener("click", async (event) => {
        const key = event.target.closest("[data-api-key-toggle]");
        const integration = event.target.closest("[data-integration-delete]");
        try {
            if (key) {
                await request("/api/v1/api-keys/" + encodeURIComponent(key.dataset.keyId) + "/" + key.dataset.apiKeyToggle, { method: "POST" });
                window.location.reload();
            } else if (integration && window.confirm("Delete this local integration?")) {
                await request("/api/v1/integrations/" + encodeURIComponent(integration.dataset.integrationId), { method: "DELETE" });
                window.location.reload();
            }
        } catch (error) { report(error); }
    });
}());
