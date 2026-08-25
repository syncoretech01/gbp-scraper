(function () {
    "use strict";

    const panel = document.querySelector("[data-system-diagnostics]");
    if (!panel) return;
    const output = panel.querySelector("[data-system-test-results]");

    function render(report) {
        output.replaceChildren();
        const summary = document.createElement("div");
        summary.className = "notice " + (report.status === "passed" ? "notice-success" : report.status === "failed" ? "notice-error" : "notice-info");
        const body = document.createElement("div");
        const title = document.createElement("strong");
        title.textContent = "Self-test: " + String(report.status || "unknown");
        body.appendChild(title);
        const detail = document.createElement("p");
        detail.textContent = "Completed in " + Number(report.duration_ms || 0) + " ms.";
        body.appendChild(detail);
        summary.appendChild(body);
        output.appendChild(summary);

        const table = document.createElement("table");
        table.className = "data-table";
        const head = document.createElement("thead");
        head.innerHTML = "<tr><th>Check</th><th>State</th><th>Details</th><th>Time</th></tr>";
        table.appendChild(head);
        const rows = document.createElement("tbody");
        (report.checks || []).forEach((check) => {
            const row = document.createElement("tr");
            [check.name, check.state, check.message, Number(check.duration_ms || 0) + " ms"].forEach((value) => {
                const cell = document.createElement("td");
                cell.textContent = String(value || "");
                row.appendChild(cell);
            });
            rows.appendChild(row);
        });
        table.appendChild(rows);
        const wrap = document.createElement("div");
        wrap.className = "table-wrap";
        wrap.appendChild(table);
        output.appendChild(wrap);
    }

    async function run(mode, button) {
        const includeNetwork = mode === "network";
        const includeBrowser = mode === "browser";
        panel.setAttribute("aria-busy", "true");
        button.disabled = true;
        output.textContent = includeBrowser
            ? "Launching a headless browser to verify the runtime… this can take a few seconds"
            : "Running bounded local diagnostics…";
        try {
            const endpoint = panel.dataset.selfTestEndpoint +
                "?include_network=" + (includeNetwork ? "true" : "false") +
                "&include_browser=" + (includeBrowser ? "true" : "false");
            const response = await fetch(endpoint, {
                method: "POST",
                credentials: "same-origin",
                headers: { "Accept": "application/json", "X-CSRF-Token": panel.dataset.csrfToken || "" }
            });
            const payload = await response.json();
            if (!response.ok) throw new Error((payload.error && payload.error.message) || "Self-test failed");
            render(payload.data || {});
        } catch (error) {
            output.textContent = error.message || "Self-test failed";
            if (window.GMapsApp) window.GMapsApp.toast(output.textContent, "error");
        } finally {
            panel.removeAttribute("aria-busy");
            button.disabled = false;
        }
    }

    const updateOutput = document.querySelector("[data-system-update-output]");

    async function showUpdateInfo(button) {
        if (!updateOutput) return;
        button.disabled = true;
        updateOutput.textContent = "Reading the locally recorded build version…";
        try {
            const response = await fetch("/api/v1/system/update-info", {
                credentials: "same-origin",
                headers: { "Accept": "application/json" }
            });
            const payload = await response.json();
            if (!response.ok) throw new Error((payload.error && payload.error.message) || "Update info unavailable");
            const data = payload.data || {};
            updateOutput.textContent =
                "Installed: " + String(data.installed_version || "unknown") +
                " · Latest: " + String(data.latest_version || "not checked") +
                " · " + String(data.message || "");
        } catch (error) {
            updateOutput.textContent = error.message || "Update info unavailable";
            if (window.GMapsApp) window.GMapsApp.toast(updateOutput.textContent, "error");
        } finally {
            button.disabled = false;
        }
    }

    panel.addEventListener("click", (event) => {
        const button = event.target.closest("[data-system-self-test]");
        if (!button) return;
        run(button.dataset.systemSelfTest, button);
    });

    document.addEventListener("click", (event) => {
        const button = event.target.closest("[data-system-update-info]");
        if (!button) return;
        event.preventDefault();
        showUpdateInfo(button);
    });
}());
