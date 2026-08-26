/*
 * app-monitor.js — the live operations console, the benchmark report, and the
 * two presentation fixes the jobs queue needs.
 *
 * Both pages are read-only surfaces over endpoints that already exist. Nothing
 * here fetches a library, injects a stylesheet, or registers an inline handler:
 * the local CSP allows script from 'self' only, so every listener is attached
 * with addEventListener and every chart is inline SVG built with the DOM API.
 *
 * Defensive binding is the rule, not the exception. The adaptive-coverage
 * engine, the per-cell coverage-confidence fields, the benchmark evidence, and
 * the optional benchmark history endpoint are each absent in some builds. A
 * missing endpoint, an error status, or an unrecognised payload always leaves
 * its panel hidden and never disturbs the rest of the page.
 */
(function () {
    "use strict";

    const SVG_NS = "http://www.w3.org/2000/svg";

    /* The words this product says out loud for a lifecycle value. "partial" is
     * the one that matters: printed raw it reads as damaged data, when what it
     * actually means is that the run stopped at a limit and kept everything it
     * had collected. The server renders the same words; this map exists so a
     * live state change does not regress the page to the database value. */
    const STATE_WORDS = {
        completed: "Finished",
        partial: "Stopped early",
        failed: "Failed",
        cancelled: "Cancelled",
        canceled: "Cancelled",
        cancelling: "Stopping",
        running: "Running",
        starting: "Starting",
        paused: "Paused",
        queued: "Queued",
        draft: "Not started"
    };

    function stateWord(state) {
        const key = String(state == null ? "" : state).toLowerCase();

        return STATE_WORDS[key] || state;
    }

    function number(value) {
        const parsed = Number(value);

        return Number.isFinite(parsed) ? parsed : 0;
    }

    function element(name, className, text) {
        const node = document.createElement(name);
        if (className) node.className = className;
        if (text != null) node.textContent = String(text);

        return node;
    }

    function svgNode(name, attributes) {
        const node = document.createElementNS(SVG_NS, name);
        Object.keys(attributes || {}).forEach((key) => node.setAttribute(key, String(attributes[key])));

        return node;
    }

    async function readJSON(url) {
        const response = await fetch(url, { headers: { Accept: "application/json" }, credentials: "same-origin" });
        if (!response.ok) {
            const error = new Error("HTTP " + response.status);
            error.status = response.status;
            throw error;
        }
        const payload = await response.json();

        return (payload && payload.data) || payload;
    }

    /* --------------------------------------------------------------------
     * Chart. One series is drawn as a filled area plus its line, the second
     * as a dashed line, both in a unit viewBox stretched by the CSS box.
     * preserveAspectRatio="none" would normally distort the strokes, so every
     * stroked element carries vector-effect="non-scaling-stroke" and no text
     * is placed inside the SVG — labels live in the surrounding HTML.
     * ------------------------------------------------------------------ */
    const CHART_W = 100;
    const CHART_H = 40;

    function chartPath(values, peak, close) {
        const step = values.length > 1 ? CHART_W / (values.length - 1) : 0;
        const y = (value) => CHART_H - 1 - (Math.max(0, value) / peak) * (CHART_H - 2);
        let d = "";
        values.forEach((value, index) => {
            const x = values.length > 1 ? index * step : CHART_W / 2;
            d += (index === 0 ? "M" : "L") + x.toFixed(2) + " " + y(value).toFixed(2);
        });
        if (close && values.length) {
            const lastX = values.length > 1 ? (values.length - 1) * step : CHART_W / 2;
            d += "L" + lastX.toFixed(2) + " " + CHART_H + "L0 " + CHART_H + "Z";
        }

        return d;
    }

    // series: [{ values, kind }] where the first entry is the emphasised one.
    function buildChart(series, labels, options) {
        const settings = options || {};
        const primary = series[0] ? series[0].values : [];
        if (!primary.length) return null;

        let peak = settings.peak || 0;
        series.forEach((entry) => entry.values.forEach((value) => { peak = Math.max(peak, value); }));
        if (peak <= 0) peak = 1;

        const root = svgNode("svg", {
            class: settings.className || "ops-chart",
            viewBox: "0 0 " + CHART_W + " " + CHART_H,
            preserveAspectRatio: "none",
            role: "img",
            focusable: "false"
        });
        root.setAttribute("aria-label", settings.label || "Trend");

        [0, 0.5, 1].forEach((fraction) => {
            const y = (1 - fraction) * (CHART_H - 2) + 1;
            root.appendChild(svgNode("line", {
                class: "ops-chart-grid", x1: 0, x2: CHART_W, y1: y.toFixed(2), y2: y.toFixed(2),
                "vector-effect": "non-scaling-stroke"
            }));
        });

        if (settings.area !== false) {
            root.appendChild(svgNode("path", { class: "ops-chart-area", d: chartPath(primary, peak, true) }));
        }

        series.forEach((entry) => {
            const path = svgNode("path", {
                class: "ops-chart-line", d: chartPath(entry.values, peak, false),
                "vector-effect": "non-scaling-stroke"
            });
            if (entry.kind) path.setAttribute("data-series", entry.kind);
            root.appendChild(path);
        });

        // Native SVG tooltips: one transparent column per sample, no JS needed.
        const width = CHART_W / primary.length;
        primary.forEach((_, index) => {
            const hit = svgNode("rect", {
                class: "ops-chart-hit", x: (index * width).toFixed(3), y: 0,
                width: width.toFixed(3), height: CHART_H
            });
            const title = svgNode("title", {});
            title.textContent = labels[index] || "";
            hit.appendChild(title);
            root.appendChild(hit);
        });

        return root;
    }

    /* ====================================================================
     * LIVE OPERATIONS CONSOLE
     * ================================================================== */
    function initConsole() {
        const monitor = document.querySelector("[data-job-monitor]");
        if (!monitor) return;

        const endpoint = monitor.dataset.eventsEndpoint;
        const progressEndpoint = monitor.dataset.progressEndpoint;
        if (!endpoint && !progressEndpoint) return;

        let source;
        let polling;
        const announce = monitor.querySelector("[data-monitor-announcement]");

        /* The worker reports "not reported by worker" (and friends) for context
         * it never observed. Those phrases are engineering status, not data:
         * on a run that finished perfectly they read as breakage. Every one of
         * them is rewritten to the design system's inline absence — an em dash
         * at reduced weight — which is the same rule the benchmark report
         * follows. The metric itself is never removed, only its non-answer. */
        const EMPTY_VALUES = [
            "not reported by worker", "not reported", "not reported yet", "not recorded",
            "not measured", "not available", "not started", "not created yet", "no data",
            "no saved area", "not enough data", "none", "unknown", "n/a", "-", "–", "—", ""
        ];

        function markEmpty(node) {
            const text = node.textContent.trim();
            const empty = EMPTY_VALUES.indexOf(text.toLowerCase()) >= 0;
            node.classList.toggle("empty-inline", empty);
            // Only a leaf is rewritten: a value that wraps its own markup (a
            // percentage span inside a bold, say) would lose that markup.
            if (empty && text !== "—" && node.children.length === 0) node.textContent = "—";
        }

        // A live update only ever replaces a value it actually carries. The
        // server already rendered a correct figure for everything else, so
        // blanking a field the stream happens not to include would make the
        // console look emptier the longer it stays open.
        function put(name, value) {
            if (value == null || value === "") return;
            monitor.querySelectorAll('[data-progress-field="' + name + '"]').forEach((node) => {
                node.textContent = String(value);
                markEmpty(node);
            });
        }

        // The "working on …" line is only worth a row when the worker has
        // actually named a task; otherwise it is two placeholders in a trench
        // coat and the progress bar above already says everything.
        function syncCurrentLine() {
            const line = monitor.querySelector(".ops-current");
            if (!line) return;
            const values = line.querySelectorAll("[data-progress-field]");
            let known = false;
            values.forEach((node) => { if (!node.classList.contains("empty-inline")) known = true; });
            line.hidden = !known;
        }

        monitor.querySelectorAll(
            "[data-progress-field], .ops-fact dd, .ops-stage-metric dd, .ops-current strong, .stat-value, .ops-readout b"
        ).forEach(markEmpty);
        syncCurrentLine();

        const TERMINAL_STATES = ["completed", "failed", "cancelled", "canceled", "ok", "archived"];

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

        // The server renders a human stage label; a live snapshot carries the
        // raw identifier, so humanise it here or the header regresses to
        // reading like a database value (e.g. "saving_exporting").
        const stageLabels = {
            preparing_queries: "Preparing queries",
            generating_grid: "Generating grid",
            searching_maps: "Searching Maps",
            extracting_details: "Extracting details",
            crawling_websites: "Crawling websites",
            extracting_contacts: "Extracting contacts",
            deduplicating: "Deduplicating",
            saving_exporting: "Saving and exporting",
        };

        function humanStage(stage) {
            if (!stage) return stage;
            if (stageLabels[stage]) return stageLabels[stage];
            const words = String(stage).replace(/_/g, " ").trim();
            if (!words) return stage;

            return words.charAt(0).toUpperCase() + words.slice(1);
        }

        // The outcome banner at the top of the page was rendered for the state
        // the page loaded in. When the stream says the run has since stopped,
        // the banner is stale: say so and offer a refresh rather than reloading
        // under an operator who may be typing in the notes field.
        const renderedState = String(monitor.querySelector("[data-outcome]")
            ? monitor.querySelector("[data-outcome]").dataset.outcome : "").toLowerCase();
        const refreshNotice = monitor.querySelector("[data-outcome-refresh]");
        const refreshButton = monitor.querySelector("[data-outcome-refresh-action]");
        if (refreshButton) {
            refreshButton.addEventListener("click", () => { window.location.reload(); });
        }

        function noteOutcomeChanged(state) {
            if (!refreshNotice || !state) return;
            const next = String(state).toLowerCase();
            if (next === renderedState || TERMINAL_STATES.indexOf(next) < 0) return;
            const title = refreshNotice.querySelector("[data-outcome-refresh-title]");
            if (title) title.textContent = "This run has stopped: " + stateWord(next).toLowerCase() + ".";
            refreshNotice.hidden = false;
        }

        function render(snapshot) {
            if (!snapshot) return;
            put("state", stateWord(snapshot.state));
            put("stage", humanStage(snapshot.stage));
            const counters = snapshot.counters || {};
            const results = snapshot.results || {};
            const execution = snapshot.execution || {};
            const tasks = execution.tasks || {};
            const worker = execution.progress || {};
            put("records", counters.records == null ? results.rows : counters.records);
            put("unique", counters.unique_records == null ? results.unique_businesses : counters.unique_records);
            put("emails", counters.emails == null ? results.with_email : counters.emails);
            put("rate", worker.places_per_minute == null ? snapshot.rates && snapshot.rates.places_per_minute : worker.places_per_minute);
            const finished = TERMINAL_STATES.indexOf(String(snapshot.state || "").toLowerCase()) >= 0;
            let eta = worker.eta_seconds == null ? snapshot.eta_seconds : worker.eta_seconds;
            if (eta == null) eta = deriveETASeconds(tasks);
            if (finished) put("eta", "Finished");
            else if (eta != null) put("eta", Math.max(0, Math.ceil(eta / 60)) + " min");
            else if (tasks.total) put("eta", "Calculating");
            put("tasks-complete", tasks.completed);
            put("tasks-total", tasks.total);
            put("tasks-failed", tasks.failed);
            put("tasks-skipped", tasks.skipped == null ? 0 : tasks.skipped);
            put("tasks-retries", tasks.retries == null ? worker.retries : tasks.retries);
            if (tasks.total != null) {
                const outstanding = Math.max(0, number(tasks.total) - number(tasks.completed) -
                    number(tasks.failed) - number(tasks.skipped));
                put("tasks-remaining", outstanding);
            }
            put("current-query", worker.current_query);
            put("current-cell", worker.current_cell);
            // The unit belongs to the value, not to the markup around it: an
            // unreported reading has to be able to become a bare em dash.
            put("cpu", worker.cpu_percent == null ? null : worker.cpu_percent + "%");
            put("disk-free", worker.disk_free_bytes);
            put("worker-concurrency", worker.effective_workers == null ? null : worker.effective_workers + " / " + worker.desired_workers);
            // Failed searches are only worth colouring when there are some.
            monitor.querySelectorAll("[data-failed-readout]").forEach((readout) => {
                const value = readout.querySelector('[data-progress-field="tasks-failed"]');
                if (!value) return;
                if (number(value.textContent) > 0) readout.dataset.emphasis = "danger";
                else readout.removeAttribute("data-emphasis");
            });
            // The state pill lives in the status header and in the top bar, so
            // every copy is restyled together.
            if (snapshot.state) {
                const state = String(snapshot.state).toLowerCase().replace(/[^a-z0-9-]/g, "");
                document.querySelectorAll('[data-progress-field="state"]').forEach((pill) => {
                    if (pill.classList.contains("status")) pill.className = "status status-" + (state || "queued");
                    pill.textContent = stateWord(snapshot.state);
                });
                noteOutcomeChanged(snapshot.state);
            }

            let percent = null;
            if (snapshot.percent != null && Number.isFinite(Number(snapshot.percent))) {
                percent = Math.max(0, Math.min(100, Number(snapshot.percent)));
                document.querySelectorAll('[data-progress-field="percent"]').forEach((node) => {
                    node.textContent = String(Math.round(percent));
                });
                monitor.querySelectorAll("[data-progress-bar]").forEach((bar) => {
                    const fill = bar.querySelector(".progress-bar") || bar;
                    fill.style.setProperty("--progress", percent + "%");
                    bar.setAttribute("aria-valuenow", String(Math.round(percent)));
                });
            }

            if (snapshot.stage_index != null) {
                monitor.querySelectorAll("[data-pipeline-stage]").forEach((step) => {
                    const order = Number(step.dataset.pipelineOrder);
                    const active = Number(snapshot.stage_index) || 0;
                    step.dataset.state = order < active ? "complete" : order === active ? "active" : "pending";
                });
            }

            syncCurrentLine();

            if (announce && snapshot.state) {
                announce.textContent = "Job " + stateWord(snapshot.state) +
                    (percent == null ? "" : ", " + Math.round(percent) + "% complete") +
                    (snapshot.stage ? ", stage " + humanStage(snapshot.stage) : "") + ".";
            }
        }

        /* ----------------------------------------------------------------
         * Log viewer.
         *
         * The server classifies every event into one of the ten operator log
         * levels and resolves its target link, so nothing here re-implements
         * that mapping: a streamed line and a server-rendered line are built
         * from the same two fields and read identically.
         * -------------------------------------------------------------- */
        const logViewer = monitor.querySelector("[data-log-viewer]");
        const logStatus = monitor.querySelector("[data-log-status]");
        const autoscrollBox = monitor.querySelector("[data-log-autoscroll]");

        function followingTail() {
            return !autoscrollBox || autoscrollBox.checked;
        }

        function scrollLogToTail() {
            if (logViewer && followingTail()) logViewer.scrollTop = logViewer.scrollHeight;
        }

        function appendLog(entry) {
            if (!logViewer || !entry) return;
            const level = entry.level || entry.severity || "information";
            const line = element("div", "log-line log-" + level);
            line.dataset.logLine = "";
            line.dataset.logLevel = level;

            const time = element("time", null, entry.occurred_at || "");
            if (entry.occurred_at) time.setAttribute("datetime", entry.occurred_at);
            line.appendChild(time);
            line.appendChild(element("span", "log-level", level));

            const body = element("span", null, entry.message || "");
            if (entry.target_url) {
                body.appendChild(document.createTextNode(" "));
                const link = element("a", "log-target", "Open affected item");
                link.href = entry.target_url;
                link.dataset.endpoint = entry.target_url;
                body.appendChild(link);
            }
            line.appendChild(body);

            // The empty-state line is a placeholder, not a record: the first
            // streamed entry replaces it rather than stacking under it.
            const placeholder = logViewer.querySelector(".log-line:not([data-log-line])");
            if (placeholder) placeholder.remove();

            logViewer.appendChild(line);
            scrollLogToTail();
        }

        if (autoscrollBox) {
            autoscrollBox.addEventListener("change", scrollLogToTail);
            scrollLogToTail();
        }

        const copyButton = monitor.querySelector("[data-log-copy]");
        if (copyButton && logViewer) {
            copyButton.addEventListener("click", () => {
                const lines = Array.prototype.map.call(
                    logViewer.querySelectorAll("[data-log-line]"),
                    (line) => Array.prototype.map.call(
                        line.querySelectorAll("time, span"),
                        (cell) => cell.textContent.trim()
                    ).join("\t")
                );
                const text = lines.join("\n");
                if (!text) {
                    if (logStatus) logStatus.textContent = "There is nothing to copy for the current filter.";

                    return;
                }
                // Clipboard access can be refused (an insecure origin, or a
                // denied permission). Saying so is better than a button that
                // silently does nothing.
                const report = (message) => { if (logStatus) logStatus.textContent = message; };
                if (navigator.clipboard && navigator.clipboard.writeText) {
                    navigator.clipboard.writeText(text).then(
                        () => report(lines.length + " log line(s) copied to the clipboard."),
                        () => report("The browser refused clipboard access; use Download logs instead.")
                    );

                    return;
                }
                report("This browser does not allow clipboard writes; use Download logs instead.");
            });
        }

        // Stream frames arrive in two shapes: a whole job snapshot, and a
        // worker frame nested under "progress". Only a nested object that
        // actually looks like a snapshot is unwrapped — otherwise the reader
        // would mistake the worker's progress block for the job and report a
        // job with no state at nought percent.
        function snapshotOf(data) {
            const nested = data && data.progress;
            if (nested && typeof nested === "object" && (nested.state != null || nested.percent != null)) {
                return nested;
            }

            return data;
        }

        // The stream carries two shapes on purpose. "snapshot" is the whole
        // job; every other frame is one durable lifecycle event, named after
        // the worker's own event type ("proxy-failure", "task-pool", …), so
        // the frame name can never be used to recognise a log line. An event
        // is recognised by what it carries instead: a message and a timestamp.
        function isLogFrame(name, data) {
            if (name === "snapshot") return false;

            return Boolean(data && typeof data === "object" &&
                data.message != null && data.occurred_at != null && data.state == null);
        }

        function consume(event) {
            let payload;
            try { payload = JSON.parse(event.data); } catch (_) { return; }
            const data = payload.data || payload;
            if (isLogFrame(event.type, data)) appendLog(data);
            else render(snapshotOf(data));
        }

        async function pollProgress() {
            if (!progressEndpoint || document.hidden) return;
            try {
                render(await readJSON(progressEndpoint));
            } catch (_) {
                if (announce) announce.textContent = "Live progress is temporarily unavailable; retrying.";
            }
        }

        function startPolling() {
            if (polling || !progressEndpoint) return;
            pollProgress();
            polling = window.setInterval(pollProgress, 3000);
        }

        /* ----------------------------------------------------------------
         * Adaptive coverage panel.
         *
         * GET /api/v1/jobs/{id}/coverage belongs to the adaptive discovery
         * engine and is absent on installations that do not run it. The panel
         * therefore stays hidden unless a well-formed payload arrives; a 404,
         * a 501, a transport error, or an unusable body leaves the console
         * exactly as the server rendered it.
         * -------------------------------------------------------------- */
        const coveragePanel = monitor.querySelector("[data-coverage-panel]");
        const coverageEndpoint = monitor.dataset.coverageEndpoint;
        let coverageAvailable = Boolean(coveragePanel && coverageEndpoint);

        function totalTile(value, label, tone) {
            const tile = element("div", "ops-total");
            if (tone) tile.dataset.tone = tone;
            tile.appendChild(element("b", null, value));
            tile.appendChild(element("span", null, label));

            return tile;
        }

        function renderCoverageTotals(totals) {
            const target = coveragePanel.querySelector("[data-coverage-totals]");
            if (!target) return;
            const done = number(totals.tasks_done);
            const failed = number(totals.tasks_failed);
            const skipped = number(totals.tasks_skipped);
            const tiles = [
                totalTile(done + " / " + number(totals.tasks_total), "searches finished"),
                totalTile(number(totals.rows_added), "rows added"),
                totalTile(number(totals.rows_replaced), "rows replaced"),
                totalTile(number(totals.duplicates_skipped), "duplicates skipped"),
                totalTile(failed, "searches failed", failed > 0 ? "danger" : ""),
                totalTile(skipped, "searches skipped", skipped > 0 ? "warning" : ""),
                totalTile(number(totals.expansions_added), "expansions added")
            ];
            // Refinements and truncation are newer fields, and both are zero on
            // most runs. They join the strip only when they carry a fact.
            if (number(totals.refinements_added) > 0) {
                tiles.push(totalTile(number(totals.refinements_added), "refinements added"));
            }
            if (number(totals.tasks_truncated) > 0) {
                tiles.push(totalTile(number(totals.tasks_truncated), "capped result sets", "warning"));
            }
            target.replaceChildren.apply(target, tiles);
        }

        function renderCoverageSaturation(saturation) {
            const target = coveragePanel.querySelector("[data-coverage-saturation]");
            if (!target) return;
            target.replaceChildren();
            if (!saturation || saturation.enabled !== true) {
                target.appendChild(element("span", null,
                    "Automatic stop is off for this job; every generated search will run."));

                return;
            }
            const ratio = Number(saturation.current_new_ratio);
            target.appendChild(element("strong", null,
                Number.isFinite(ratio) ? (ratio * 100).toFixed(1) + "% new" : "no samples yet"));
            target.appendChild(element("span", null,
                "over the last " + number(saturation.window_samples || saturation.window) +
                " of " + number(saturation.window) + " searches; stops below " +
                (number(saturation.min_new_ratio) * 100).toFixed(1) + "% new."));
            if (saturation.stopped === true) {
                const badge = element("span", "badge badge-warning", saturation.reason === "empty-area"
                    ? "stopped early: nothing left to find here"
                    : "stopped early: the area was already covered");
                target.appendChild(badge);
            }
        }

        function renderCoverageTrend(trend) {
            const frame = coveragePanel.querySelector("[data-coverage-trend-frame]");
            const target = coveragePanel.querySelector("[data-coverage-trend]");
            if (!frame || !target) return;
            const points = Array.isArray(trend) ? trend.slice(-60) : [];
            if (points.length < 2) {
                frame.hidden = true;

                return;
            }
            const added = points.map((point) => number(point.rows_added));
            const duplicates = points.map((point) => number(point.duplicates_skipped));
            const labels = points.map((point, index) =>
                "#" + number(point.seq || index + 1) + " · " + added[index] + " rows added · " +
                duplicates[index] + " duplicates skipped");
            const chart = buildChart(
                [{ values: added, kind: "added" }, { values: duplicates, kind: "duplicates" }],
                labels,
                { className: "ops-chart", label: "Rows added and duplicates skipped per finished query" }
            );
            if (!chart) {
                frame.hidden = true;

                return;
            }
            target.replaceChildren(chart);
            const peak = coveragePanel.querySelector("[data-coverage-peak]");
            if (peak) {
                peak.textContent = "peak " + Math.max.apply(null, added.concat(duplicates)) +
                    " rows · " + points.length + " checkpoints";
            }
            frame.hidden = false;
        }

        /* Per-cell coverage confidence is being added to this same endpoint by
         * the discovery engine. The field name is not fixed yet, so the reader
         * accepts the plausible shapes (a number, a percentage, a label, or a
         * {score,label} object) and returns null when none is present. The
         * column is revealed only if at least one row carries a value. */
        const CONFIDENCE_NUMERIC_KEYS = [
            "coverage_confidence", "confidence", "confidence_score",
            "confidence_ratio", "cell_confidence", "confidence_percent"
        ];
        const CONFIDENCE_LABEL_KEYS = [
            "confidence_label", "coverage_confidence_label", "confidence_level", "confidence_band"
        ];

        function readConfidence(entry) {
            let score = null;
            let label = "";

            for (let i = 0; i < CONFIDENCE_NUMERIC_KEYS.length; i += 1) {
                const raw = entry[CONFIDENCE_NUMERIC_KEYS[i]];
                if (raw == null) continue;
                if (typeof raw === "object") {
                    const nested = Number(raw.score == null ? raw.value : raw.score);
                    if (Number.isFinite(nested)) score = nested;
                    if (typeof raw.label === "string") label = raw.label;
                    break;
                }
                const parsed = Number(raw);
                if (Number.isFinite(parsed)) score = parsed;
                else if (typeof raw === "string" && raw) label = raw;
                break;
            }

            if (!label) {
                for (let i = 0; i < CONFIDENCE_LABEL_KEYS.length; i += 1) {
                    const raw = entry[CONFIDENCE_LABEL_KEYS[i]];
                    if (typeof raw === "string" && raw) { label = raw; break; }
                }
            }

            if (score === null && !label) return null;

            let percent = null;
            if (score !== null) {
                percent = Math.round(score <= 1 ? score * 100 : score);
                percent = Math.max(0, Math.min(100, percent));
            }

            return { percent: percent, label: label };
        }

        function confidenceCell(confidence) {
            const cell = element("td");
            cell.dataset.col = "confidence";
            if (!confidence) {
                cell.appendChild(element("span", "empty-inline", "—"));

                return cell;
            }
            const wrap = element("span", "ops-confidence");
            const percent = confidence.percent;
            if (percent != null) {
                wrap.dataset.tone = percent >= 70 ? "success" : percent >= 40 ? "warning" : "danger";
                wrap.appendChild(element("span", "num", percent + "%"));
                const track = element("span", "ops-confidence-track");
                const fill = element("span", "ops-confidence-fill");
                fill.style.setProperty("--meter", percent + "%");
                track.appendChild(fill);
                wrap.appendChild(track);
            }
            if (confidence.label) wrap.appendChild(element("span", "t-caption", confidence.label));
            cell.appendChild(wrap);

            return cell;
        }

        // Origin is "" for a seed query, "expansion:<zip>" for a neighbour the
        // engine added, and "refine:<zip>" for a re-cover of a capped cell. The
        // parent ZIP goes in the tooltip: the row already prints its own ZIP,
        // and a second truncated number in the cell reads as noise.
        function originBadge(origin) {
            const value = String(origin || "");
            if (value.indexOf("expansion:") === 0) {
                return { className: "badge badge-warning", text: "expansion", title: "Expanded from ZIP " + value.slice(10) };
            }
            if (value.indexOf("refine:") === 0) {
                return { className: "badge badge-special", text: "refinement", title: "Re-covers capped ZIP " + value.slice(7) };
            }

            return { className: "badge badge-outline", text: value || "plan", title: "Seed query from the original plan" };
        }

        function renderCoverageQueries(rows) {
            const table = coveragePanel.querySelector("[data-coverage-table]");
            const target = coveragePanel.querySelector("[data-coverage-queries]");
            if (!target) return;
            const entries = Array.isArray(rows) ? rows.slice(0, 300) : [];
            const fragment = document.createDocumentFragment();
            let anyConfidence = false;

            entries.forEach((entry) => {
                const row = element("tr");
                const state = String(entry.state || "waiting").toLowerCase().replace(/[^a-z0-9-]/g, "");

                const queryCell = element("td");
                const query = element("span", "ops-task-query", entry.query || "");
                query.title = String(entry.query || "");
                queryCell.appendChild(query);
                if (entry.truncated === true) {
                    queryCell.appendChild(element("span", "badge badge-warning", "capped"));
                }
                row.appendChild(queryCell);

                row.appendChild(element("td", "t-mono", entry.zip || "—"));

                const origin = originBadge(entry.origin);
                const originCell = element("td");
                const originTag = element("span", origin.className, origin.text);
                originTag.title = origin.title;
                originCell.appendChild(originTag);
                row.appendChild(originCell);

                const stateCell = element("td");
                stateCell.appendChild(element("span", "status status-" + (state || "waiting"), entry.state || "waiting"));
                row.appendChild(stateCell);

                const confidence = readConfidence(entry);
                if (confidence) anyConfidence = true;
                row.appendChild(confidenceCell(confidence));

                [
                    number(entry.attempts),
                    number(entry.rows_added),
                    number(entry.duplicates_skipped),
                    number(entry.seconds)
                ].forEach((value) => row.appendChild(element("td", "num", value)));

                fragment.appendChild(row);
            });

            target.replaceChildren(fragment);
            if (table) table.dataset.confidence = anyConfidence ? "true" : "false";
        }

        async function pollCoverage() {
            if (!coverageAvailable || document.hidden) return;
            let data;
            try {
                data = await readJSON(coverageEndpoint);
            } catch (error) {
                // 404/501 means this build has no coverage engine; stop asking.
                if (error.status === 404 || error.status === 501) coverageAvailable = false;
                coveragePanel.hidden = true;

                return;
            }
            if (!data || typeof data !== "object" || !data.totals || typeof data.totals !== "object") {
                coveragePanel.hidden = true;

                return;
            }
            const planned = number(data.totals.tasks_total);
            const rows = Array.isArray(data.by_query) ? data.by_query : [];
            if (planned <= 0 && !rows.length) {
                // A run with no durable plan has nothing to show; scaffolding
                // full of zeroes would be worse than an absent panel.
                coveragePanel.hidden = true;

                return;
            }
            renderCoverageTotals(data.totals);
            renderCoverageSaturation(data.saturation);
            renderCoverageTrend(data.trend);
            renderCoverageQueries(rows);
            coveragePanel.hidden = false;
        }

        /* ----------------------------------------------------------------
         * Failure classes. Read from the benchmark evidence endpoint so the
         * console and the benchmark report can never disagree about what
         * failed. Hidden whenever that endpoint is unavailable or clean.
         * -------------------------------------------------------------- */
        const failurePanel = monitor.querySelector("[data-failure-panel]");
        const benchmarkEndpoint = monitor.dataset.benchmarkEndpoint;

        async function loadFailures() {
            if (!failurePanel || !benchmarkEndpoint) return;
            let data;
            try {
                data = await readJSON(benchmarkEndpoint);
            } catch (_) {
                failurePanel.hidden = true;

                return;
            }
            const failures = data && Array.isArray(data.failures) ? data.failures : [];
            if (!failures.length) {
                failurePanel.hidden = true;

                return;
            }
            const list = failurePanel.querySelector("[data-failure-list]");
            if (!list) return;
            const items = failures.slice(0, 12).map((failure) => {
                const item = element("li", "ops-failure");
                item.appendChild(element("b", null, failure.class || "unclassified"));
                const counts = element("span", "cluster");
                counts.appendChild(element("span", "badge badge-danger", number(failure.count) + "×"));
                counts.appendChild(element("span", "badge", number(failure.retries) + " retried"));
                item.appendChild(counts);
                if (failure.sample) item.appendChild(element("small", null, failure.sample));

                return item;
            });
            list.replaceChildren.apply(list, items);
            failurePanel.hidden = false;
        }

        pollCoverage();
        loadFailures();
        const slowTimer = window.setInterval(() => { pollCoverage(); loadFailures(); }, 10000);

        if (endpoint && window.EventSource) {
            source = new EventSource(endpoint, { withCredentials: true });
            ["snapshot", "state", "stage", "progress", "resource", "checkpoint", "control", "adaptive", "log"]
                .forEach((type) => source.addEventListener(type, consume));
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
            window.clearInterval(slowTimer);
        }, { once: true });
    }

    /* ====================================================================
     * BENCHMARK REPORT
     * ================================================================== */
    function initBenchmark() {
        const report = document.querySelector("[data-benchmark-report]");
        if (!report) return;

        // --- Saturation chart, drawn from the rendered table ---------------
        const frame = report.querySelector("[data-saturation-frame]");
        const chartTarget = report.querySelector("[data-saturation-chart]");
        const table = report.querySelector("[data-saturation-table]");
        if (frame && chartTarget && table) {
            const rows = Array.prototype.slice.call(table.querySelectorAll("tbody tr"));
            if (rows.length >= 2) {
                const ratios = rows.map((row) => {
                    const parsed = parseFloat(String(row.dataset.ratio || "").replace("%", ""));

                    return Number.isFinite(parsed) ? parsed : 0;
                });
                const added = rows.map((row) => number(row.dataset.rows));
                const peakRows = Math.max.apply(null, added) || 1;
                // Rows are rescaled onto the 0-100 ratio axis so both series
                // share one drawing space; the legend states the real peak.
                const scaledRows = added.map((value) => (value / peakRows) * 100);
                const labels = rows.map((row, index) =>
                    "#" + number(row.dataset.seq || index + 1) + " · " + ratios[index].toFixed(1) +
                    "% cumulative new · " + added[index] + " rows added");
                const chart = buildChart(
                    [{ values: ratios, kind: "added" }, { values: scaledRows, kind: "duplicates" }],
                    labels,
                    {
                        className: "ops-chart bench-chart", peak: 100, area: false,
                        label: "Cumulative new ratio across the run"
                    }
                );
                if (chart) {
                    chartTarget.replaceChildren(chart);
                    frame.hidden = false;
                    const legend = frame.querySelector(".ops-chart-legend span:last-child");
                    if (legend) legend.appendChild(document.createTextNode(" (peak " + peakRows + ")"));
                }
            }
        }

        // --- Compare with another run -------------------------------------
        const comparePanel = report.querySelector("[data-compare-panel]");
        const compareEndpoint = report.dataset.compareEndpoint;
        const baseID = report.dataset.jobId;
        if (!comparePanel || !compareEndpoint || !baseID) return;

        const candidate = comparePanel.querySelector("[data-compare-candidate]");
        const run = comparePanel.querySelector("[data-compare-run]");
        const status = comparePanel.querySelector("[data-compare-status]");
        const deltas = comparePanel.querySelector("[data-compare-deltas]");
        if (!candidate || !run || !deltas) return;

        // Every delta states which direction is good, because "fewer failures"
        // and "more unique businesses" are both improvements.
        const DELTA_FIELDS = [
            { key: "unique_businesses", label: "unique businesses", better: "up", digits: 0 },
            { key: "new_businesses_per_minute", label: "new per minute", better: "up", digits: 2 },
            { key: "duplicate_rate", label: "duplicate rate", better: "down", digits: 3 },
            { key: "tasks_failed", label: "failed tasks", better: "down", digits: 0 },
            { key: "failure_count", label: "failure events", better: "down", digits: 0 },
            { key: "retries", label: "retries", better: "down", digits: 0 },
            { key: "wall_seconds", label: "wall seconds", better: "down", digits: 0 }
        ];

        function renderDeltas(delta) {
            const tiles = DELTA_FIELDS.map((field) => {
                const value = Number(delta[field.key]);
                const tile = element("div", "bench-delta");
                const safe = Number.isFinite(value) ? value : 0;
                const trend = safe === 0 ? "flat" : (safe > 0) === (field.better === "up") ? "up" : "down";
                tile.dataset.trend = trend;
                tile.appendChild(element("b", null, (safe > 0 ? "+" : "") + safe.toFixed(field.digits)));
                tile.appendChild(element("span", null, field.label));

                return tile;
            });
            deltas.replaceChildren.apply(deltas, tiles);
            deltas.hidden = false;
        }

        run.addEventListener("click", async () => {
            const candidateID = candidate.value;
            if (!candidateID) return;
            run.setAttribute("aria-busy", "true");
            if (status) status.textContent = "Reading both reports…";
            try {
                const data = await readJSON(compareEndpoint + "?base=" + encodeURIComponent(baseID) +
                    "&candidate=" + encodeURIComponent(candidateID));
                if (!data || typeof data.delta !== "object" || data.delta === null) {
                    throw new Error("no delta");
                }
                renderDeltas(data.delta);
                if (status) {
                    status.textContent = "Candidate minus this run. Green is the better direction for each measure.";
                }
            } catch (error) {
                deltas.hidden = true;
                if (status) {
                    status.textContent = error && error.status === 501
                        ? "This build does not store comparable benchmark evidence."
                        : "That comparison could not be read. Both runs need stored benchmark evidence.";
                }
            } finally {
                run.removeAttribute("aria-busy");
            }
        });

        /* An optional benchmark history endpoint is being added elsewhere. It
         * may or may not exist in this build, so it is probed once and its
         * affordance is appended only on a usable answer. Nothing about the
         * page depends on the probe succeeding. */
        (async function probeHistory() {
            const candidates = [
                "/api/v1/jobs/" + encodeURIComponent(baseID) + "/benchmark/history",
                "/api/v1/benchmark/history?job_id=" + encodeURIComponent(baseID)
            ];
            for (let i = 0; i < candidates.length; i += 1) {
                let data;
                try {
                    data = await readJSON(candidates[i]);
                } catch (_) {
                    continue;
                }
                const entries = Array.isArray(data) ? data : (data && Array.isArray(data.entries) ? data.entries : null);
                if (!entries || !entries.length) continue;
                const link = element("a", "button button-sm");
                link.href = candidates[i];
                link.textContent = "History (" + entries.length + " recorded runs)";
                const host = comparePanel.querySelector(".bench-compare-form");
                if (host) host.insertBefore(link, host.querySelector("[data-compare-status]"));

                return;
            }
        })();
    }

    /* ====================================================================
     * JOBS QUEUE
     *
     * One presentational repair. The queue's configuration column prints the
     * server's location summary, and for a job configured from the map that
     * summary is a pair of decimal degrees — a value no operator recognises as
     * a place. JobData already stores a location label, so the durable fix is
     * for that summary to use it; until it does, the queue names the shape of
     * the area instead of its coordinates and keeps the exact original string
     * in the cell's tooltip. A summary that is already a name is left alone.
     * ================================================================== */
    function initJobsList() {
        const page = document.querySelector("[data-jobs-page]");
        if (!page) return;

        const COORD = "-?\\d{1,3}(?:\\.\\d+)?";
        const PAIR = COORD + "\\s*,\\s*" + COORD;
        const RULES = [
            { match: new RegExp("^Maps search near\\s*" + PAIR + "$", "i"), text: "One map area" },
            {
                match: new RegExp("^Fast Mode within\\s+(.+?)\\s+of\\s+" + PAIR + "$", "i"),
                text: "Fast mode · $1 around one map point"
            },
            { match: /^Grid\s+\S+\s+\((.+?)\s*km cells\)$/i, text: "Map area · $1 km squares" },
            { match: /^No geographic constraint recorded$/i, text: "No area limit" }
        ];

        page.querySelectorAll("[data-location-summary]").forEach((cell) => {
            const original = cell.textContent.trim();
            for (let i = 0; i < RULES.length; i += 1) {
                if (!RULES[i].match.test(original)) continue;
                const rewritten = original.replace(RULES[i].match, RULES[i].text);
                if (rewritten === original) return;
                cell.textContent = rewritten;
                cell.title = original;

                return;
            }
        });
    }

    initConsole();
    initBenchmark();
    initJobsList();
})();
