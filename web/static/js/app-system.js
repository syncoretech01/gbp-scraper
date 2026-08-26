/*
 * app-system.js — behaviour shared by the operations pages.
 *
 * Two independent blocks, each guarded by its own page marker so the file can
 * be loaded on System health and on Settings without either half running on
 * the wrong page:
 *
 *   [data-system-diagnostics]  the local self-test and offline update info
 *   [data-settings-nav]        the sticky section rail on Settings
 *
 * No inline handlers, no framework, no network access beyond the two local
 * endpoints the System page already exposes.
 */
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
            // Absence is rendered as an em dash, never as an engineering status
            // string: "unknown" and "not checked" read to an operator as breakage.
            const installed = String(data.installed_version || "").trim();
            const latest = String(data.latest_version || "").trim();
            const message = String(data.message || "").trim();
            const parts = [
                "Installed: " + (installed || "—"),
                "Latest: " + (latest || "—")
            ];
            if (message) parts.push(message);
            updateOutput.textContent = parts.join(" · ");
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

/*
 * Settings section rail.
 *
 * The rail is plain anchor links, so it already works with scripting off and
 * with the keyboard. This block only adds the "you are here" marker, driven by
 * whichever section is nearest the top of the scrolling region.
 */
(function () {
    "use strict";

    const nav = document.querySelector("[data-settings-nav]");
    if (!nav) return;

    const links = Array.prototype.slice.call(nav.querySelectorAll("[data-settings-nav-link]"));
    if (!links.length) return;

    const entries = links
        .map((link) => {
            const id = (link.getAttribute("href") || "").replace(/^#/, "");
            const target = id ? document.getElementById(id) : null;
            return target ? { link: link, target: target } : null;
        })
        .filter(Boolean);
    if (!entries.length) return;

    let active = null;

    function mark(entry) {
        if (entry === active) return;
        active = entry;
        entries.forEach((candidate) => {
            if (candidate === entry) {
                candidate.link.setAttribute("aria-current", "true");
            } else {
                candidate.link.removeAttribute("aria-current");
            }
        });
    }

    function nearest() {
        // The scrolling region is .app-main, so section positions are measured
        // against the viewport rather than the document.
        const threshold = 140;
        let best = entries[0];
        entries.forEach((entry) => {
            const top = entry.target.getBoundingClientRect().top;
            if (top <= threshold) best = entry;
        });
        mark(best);
    }

    let queued = false;
    function schedule() {
        if (queued) return;
        queued = true;
        window.requestAnimationFrame(() => {
            queued = false;
            nearest();
        });
    }

    const scroller = document.querySelector(".app-main") || window;
    scroller.addEventListener("scroll", schedule, { passive: true });
    window.addEventListener("resize", schedule, { passive: true });

    // Anchor clicks land before the scroll finishes, so mark the destination
    // immediately rather than waiting for the smooth scroll to settle.
    nav.addEventListener("click", (event) => {
        const link = event.target.closest("[data-settings-nav-link]");
        if (!link) return;
        const entry = entries.find((candidate) => candidate.link === link);
        if (entry) mark(entry);
    });

    nearest();
}());
