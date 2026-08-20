(function () {
    "use strict";

    const monitor = document.querySelector("[data-job-monitor]");
    if (!monitor) return;
    const endpoint = monitor.dataset.eventsEndpoint;
    const progressEndpoint = monitor.dataset.progressEndpoint;
    if (!endpoint && !progressEndpoint) return;

    let source;
    let polling;
    const announce = monitor.querySelector("[data-monitor-announcement]");

    function put(name, value) {
        monitor.querySelectorAll('[data-progress-field="' + name + '"]').forEach((node) => { node.textContent = value == null ? "—" : String(value); });
    }

    // Task-completion samples back the derived ETA used when neither the
    // worker nor the snapshot reports one: remaining tasks divided by the
    // completion rate observed across this session's updates.
    let firstSample = null;
    let lastSample = null;

    function deriveETASeconds(tasks) {
        const total = Number(tasks.total) || 0;
        const done = (Number(tasks.completed) || 0) + (Number(tasks.failed) || 0) + (Number(tasks.skipped) || 0);
        if (total <= 0 || done <= 0 || done >= total) return null;
        const now = Date.now();
        if (!firstSample || done < firstSample.done) firstSample = { at: now, done: done };
        lastSample = { at: now, done: done };
        const elapsed = (lastSample.at - firstSample.at) / 1000;
        const progressed = lastSample.done - firstSample.done;
        if (elapsed < 5 || progressed <= 0) return null;
        return Math.round(((total - done) * elapsed) / progressed);
    }

    function render(snapshot) {
        if (!snapshot) return;
        put("state", snapshot.state);
        put("stage", snapshot.stage);
        const counters = snapshot.counters || {};
        const results = snapshot.results || {};
        const execution = snapshot.execution || {};
        const tasks = execution.tasks || {};
        const worker = execution.progress || {};
        put("records", counters.records == null ? results.rows : counters.records);
        put("unique", counters.unique_records == null ? results.unique_businesses : counters.unique_records);
        put("emails", counters.emails == null ? results.with_email : counters.emails);
        put("rate", worker.places_per_minute == null ? snapshot.rates && snapshot.rates.places_per_minute : worker.places_per_minute);
        let eta = worker.eta_seconds == null ? snapshot.eta_seconds : worker.eta_seconds;
        if (eta == null) eta = deriveETASeconds(tasks);
        put("eta", eta == null ? "Calculating" : Math.max(0, Math.ceil(eta / 60)) + " min");
        put("tasks-complete", tasks.completed);
        put("tasks-total", tasks.total);
        put("tasks-failed", tasks.failed);
        put("tasks-skipped", tasks.skipped == null ? 0 : tasks.skipped);
        put("tasks-retries", tasks.retries == null ? worker.retries : tasks.retries);
        put("tasks-remaining", (tasks.pending || 0) + (tasks.running || 0));
        put("current-query", worker.current_query);
        put("current-cell", worker.current_cell);
        put("cpu", worker.cpu_percent);
        put("disk-free", worker.disk_free_bytes);
        put("worker-concurrency", worker.effective_workers == null ? null : worker.effective_workers + " / " + worker.desired_workers);
        const percent = Math.max(0, Math.min(100, Number(snapshot.percent) || 0));
        put("percent", Math.round(percent));
        monitor.querySelectorAll("[data-progress-bar]").forEach((bar) => {
            bar.style.setProperty("--progress", percent + "%");
            bar.setAttribute("aria-valuenow", String(percent));
        });
        monitor.querySelectorAll("[data-pipeline-stage]").forEach((step) => {
            const order = Number(step.dataset.pipelineOrder);
            const active = Number(snapshot.stage_index || 0);
            step.dataset.state = order < active ? "complete" : order === active ? "active" : "pending";
        });
        if (announce) announce.textContent = "Job " + snapshot.state + ", " + percent + "% complete, stage " + snapshot.stage + ".";
    }

    function appendLog(entry) {
        const viewer = monitor.querySelector("[data-log-viewer]");
        if (!viewer || !entry) return;
        const line = document.createElement("div");
        line.className = "log-line log-" + (entry.severity || "information");
        [entry.occurred_at || "", entry.severity || "info", entry.message || ""].forEach((value) => {
            const span = document.createElement("span");
            span.textContent = value;
            line.appendChild(span);
        });
        viewer.appendChild(line);
        const autoscroll = monitor.querySelector('[name="log_autoscroll"]');
        if (!autoscroll || autoscroll.checked) viewer.scrollTop = viewer.scrollHeight;
    }

    function consume(event) {
        let payload;
        try { payload = JSON.parse(event.data); } catch (_) { return; }
        const data = payload.data || payload;
        if (event.type === "log" || payload.type === "log") appendLog(data);
        else render(data.progress || data);
    }

    async function pollProgress() {
        if (!progressEndpoint || document.hidden) return;
        try {
            const response = await fetch(progressEndpoint, { headers: { Accept: "application/json" }, credentials: "same-origin" });
            if (!response.ok) throw new Error("HTTP " + response.status);
            const payload = await response.json();
            render(payload.data || payload);
        } catch (_) {
            if (announce) announce.textContent = "Live progress is temporarily unavailable; retrying.";
        }
    }

    function startPolling() {
        if (polling || !progressEndpoint) return;
        pollProgress();
        polling = window.setInterval(pollProgress, 3000);
    }

    // --- Adaptive coverage panel -------------------------------------------
    // The coverage endpoint belongs to the adaptive discovery engine and is
    // absent on installations that do not run it. The panel therefore stays
    // hidden unless a well-formed payload arrives; a 404, a transport error,
    // or an unusable body leaves the monitor exactly as it was.
    const coveragePanel = monitor.querySelector("[data-coverage-panel]");
    const coverageEndpoint = coveragePanel && coveragePanel.dataset.coverageEndpoint;
    let coverageAvailable = true;

    function number(value) {
        const parsed = Number(value);
        return Number.isFinite(parsed) ? parsed : 0;
    }

    function coverageTile(label, value) {
        const tile = document.createElement("div");
        const amount = document.createElement("strong");
        amount.textContent = String(value);
        const caption = document.createElement("span");
        caption.textContent = label;
        tile.append(amount, caption);
        return tile;
    }

    function renderCoverageTotals(totals) {
        const target = coveragePanel.querySelector("[data-coverage-totals]");
        if (!target) return;
        const done = number(totals.tasks_done);
        const tiles = [
            coverageTile("of " + number(totals.tasks_total) + " queries done", done),
            coverageTile("rows added", number(totals.rows_added)),
            coverageTile("rows replaced", number(totals.rows_replaced)),
            coverageTile("duplicates skipped", number(totals.duplicates_skipped)),
            coverageTile("queries failed", number(totals.tasks_failed)),
            coverageTile("queries skipped", number(totals.tasks_skipped)),
            coverageTile("expansions added", number(totals.expansions_added))
        ];
        target.replaceChildren(...tiles);
    }

    function renderCoverageSaturation(saturation) {
        const target = coveragePanel.querySelector("[data-coverage-saturation]");
        if (!target) return;
        target.replaceChildren();
        if (!saturation || saturation.enabled !== true) {
            target.textContent = "Automatic stop is off for this job; every generated query will run.";
            return;
        }
        const current = document.createElement("strong");
        const ratio = Number(saturation.current_new_ratio);
        current.textContent = Number.isFinite(ratio) ? (ratio * 100).toFixed(1) + "% new" : "no samples yet";
        const detail = document.createElement("span");
        detail.textContent = "over the last " + number(saturation.window) +
            " queries; stops below " + (number(saturation.min_new_ratio) * 100).toFixed(1) + "% new.";
        target.append(current, detail);
        if (saturation.stopped === true) {
            const badge = document.createElement("span");
            badge.className = "status status-completed";
            badge.textContent = "stopped on saturation";
            target.appendChild(badge);
        }
    }

    function renderCoverageTrend(trend) {
        const target = coveragePanel.querySelector("[data-coverage-trend]");
        if (!target) return;
        target.replaceChildren();
        const points = Array.isArray(trend) ? trend.slice(-40) : [];
        if (!points.length) {
            target.hidden = true;
            return;
        }
        target.hidden = false;
        let peak = 1;
        points.forEach((point) => {
            peak = Math.max(peak, number(point.rows_added), number(point.duplicates_skipped));
        });
        points.forEach((point) => {
            [["added", number(point.rows_added)], ["duplicates", number(point.duplicates_skipped)]].forEach((pair) => {
                const bar = document.createElement("span");
                bar.dataset.kind = pair[0];
                bar.style.setProperty("--value", Math.max(2, Math.round((pair[1] / peak) * 100)) + "%");
                bar.dataset.label = "#" + number(point.seq) + ": " + pair[1] + " " +
                    (pair[0] === "added" ? "rows added" : "duplicates skipped");
                target.appendChild(bar);
            });
        });
    }

    function renderCoverageQueries(rows) {
        const target = coveragePanel.querySelector("[data-coverage-queries]");
        if (!target) return;
        target.replaceChildren();
        const entries = Array.isArray(rows) ? rows.slice(0, 200) : [];
        entries.forEach((entry) => {
            const row = document.createElement("tr");
            const state = String(entry.state || "waiting").toLowerCase().replace(/[^a-z0-9-]/g, "");
            [
                String(entry.query || ""),
                String(entry.zip || ""),
                String(entry.origin || ""),
                null,
                String(number(entry.attempts)),
                String(number(entry.rows_added)),
                String(number(entry.duplicates_skipped)),
                String(number(entry.seconds))
            ].forEach((value, index) => {
                const cell = document.createElement("td");
                if (index === 3) {
                    const badge = document.createElement("span");
                    badge.className = "status status-" + (state || "waiting");
                    badge.textContent = String(entry.state || "waiting");
                    cell.appendChild(badge);
                } else cell.textContent = value;
                row.appendChild(cell);
            });
            target.appendChild(row);
        });
    }

    async function pollCoverage() {
        if (!coveragePanel || !coverageEndpoint || !coverageAvailable || document.hidden) return;
        let payload;
        try {
            const response = await fetch(coverageEndpoint, { headers: { Accept: "application/json" }, credentials: "same-origin" });
            if (!response.ok) {
                // 404 means this build has no coverage engine; stop asking.
                if (response.status === 404 || response.status === 501) coverageAvailable = false;
                coveragePanel.hidden = true;
                return;
            }
            payload = await response.json();
        } catch (_) {
            coveragePanel.hidden = true;
            return;
        }
        const data = (payload && payload.data) || payload;
        if (!data || typeof data !== "object" || !data.totals || typeof data.totals !== "object") {
            coveragePanel.hidden = true;
            return;
        }
        renderCoverageTotals(data.totals);
        renderCoverageSaturation(data.saturation);
        renderCoverageTrend(data.trend);
        renderCoverageQueries(data.by_query);
        coveragePanel.hidden = false;
    }

    if (coveragePanel && coverageEndpoint) {
        pollCoverage();
        const coverageTimer = window.setInterval(pollCoverage, 10000);
        window.addEventListener("pagehide", () => window.clearInterval(coverageTimer), { once: true });
    }

    if (endpoint && window.EventSource) {
        source = new EventSource(endpoint, { withCredentials: true });
        ["snapshot", "state", "stage", "progress", "resource", "checkpoint", "control", "adaptive", "log"].forEach((type) => source.addEventListener(type, consume));
        source.onopen = function () {
            if (polling) { window.clearInterval(polling); polling = null; }
        };
        source.onerror = function () {
            if (announce) announce.textContent = "Live connection interrupted; reconnecting automatically.";
            startPolling();
        };
    } else startPolling();

    window.addEventListener("pagehide", () => {
        if (source) source.close();
        if (polling) window.clearInterval(polling);
    }, { once: true });
})();
