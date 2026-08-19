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
        const eta = worker.eta_seconds == null ? snapshot.eta_seconds : worker.eta_seconds;
        put("eta", eta == null ? "Calculating" : Math.max(0, Math.ceil(eta / 60)) + " min");
        put("tasks-complete", tasks.completed);
        put("tasks-total", tasks.total);
        put("tasks-failed", tasks.failed);
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
