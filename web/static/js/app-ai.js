(function () {
    "use strict";

    // The optional local-AI console degrades invisibly. The card ships hidden;
    // it is revealed only after /api/v1/ai/status reports an enabled and
    // reachable local Ollama server, so an operator who never installs Ollama
    // sees no dead control and no failed request blocks the Settings page.
    const console_ = document.querySelector("[data-ai-console]");
    if (!console_) return;

    const csrf = console_.dataset.csrfToken || "";
    const state = console_.querySelector("[data-ai-state]");
    const taskPicker = console_.querySelector("[data-ai-task]");
    const taskHelp = console_.querySelector("[data-ai-task-help]");
    const input = console_.querySelector("[data-ai-input]");
    const context = console_.querySelector("[data-ai-context]");
    const output = console_.querySelector("[data-ai-output]");
    const runButton = console_.querySelector("[data-ai-run]");
    const clearButton = console_.querySelector("[data-ai-clear]");

    const TASKS = {
        scrape_configuration: {
            help: "Describe what you want to collect and where. The model proposes keywords, a location, and coverage settings for you to review.",
            placeholder: "Independent dental practices within 10 km of central San Francisco that have a website"
        },
        classify_business: {
            help: "Paste a business record as context. The model proposes categories and a website-quality band from the supplied evidence only.",
            placeholder: "Classify this business and judge how strong its web presence looks."
        },
        explain_quality: {
            help: "Paste the score breakdown from a result's Quality panel as context.",
            placeholder: "Explain why this business scores the way it does and what would raise it."
        },
        explain_duplicate: {
            help: "Paste both candidate records as context. The model compares only what you supply.",
            placeholder: "Are these the same business? Explain the matching and conflicting evidence."
        },
        summarize_business: {
            help: "Paste a description, categories, or crawled website text as context.",
            placeholder: "Summarize what this business does and who it serves."
        },
        summarize_changes: {
            help: "Paste the change history of a business as context.",
            placeholder: "Summarize what changed and whether anything needs a follow-up."
        },
        suggest_coverage: {
            help: "Describe the coverage you already have. Suggestions are proposals, not verified gaps.",
            placeholder: "I already cover San Francisco and Oakland for dentists. What am I missing?"
        }
    };

    function syncTask() {
        const task = TASKS[taskPicker.value] || TASKS.scrape_configuration;
        taskHelp.textContent = task.help;
        input.placeholder = task.placeholder;
    }

    function setState(label, tone) {
        state.textContent = label;
        state.className = "badge" + (tone ? " badge-" + tone : "");
    }

    // Rendering is done with real elements rather than markup strings so model
    // output can never be interpreted as HTML.
    function renderValue(value) {
        if (Array.isArray(value)) {
            const list = document.createElement("ul");
            value.forEach((item) => {
                const entry = document.createElement("li");
                entry.appendChild(renderValue(item));
                list.appendChild(entry);
            });
            return list;
        }
        if (value && typeof value === "object") {
            const list = document.createElement("dl");
            list.className = "key-value";
            Object.keys(value).forEach((key) => {
                const term = document.createElement("dt");
                term.textContent = key.replace(/_/g, " ");
                const detail = document.createElement("dd");
                detail.appendChild(renderValue(value[key]));
                list.append(term, detail);
            });
            return list;
        }
        const text = document.createElement("span");
        text.textContent = value === null || value === undefined ? "—" : String(value);
        return text;
    }

    function renderResult(payload) {
        output.replaceChildren();
        const caution = document.createElement("div");
        caution.className = "notice notice-info";
        const cautionBody = document.createElement("div");
        const cautionTitle = document.createElement("strong");
        cautionTitle.textContent = "Suggestion from " + (payload.model || "the local model");
        const cautionText = document.createElement("p");
        cautionText.textContent = "Generated locally in " + Number(payload.duration_ms || 0) +
            " ms. Nothing here is verified — review it before you act on it.";
        cautionBody.append(cautionTitle, cautionText);
        caution.appendChild(cautionBody);
        output.appendChild(caution);
        output.appendChild(renderValue(payload.result));

        const copy = document.createElement("button");
        copy.className = "button button-small";
        copy.type = "button";
        copy.textContent = "Copy JSON";
        copy.addEventListener("click", () => {
            const text = JSON.stringify(payload.result, null, 2);
            if (navigator.clipboard && navigator.clipboard.writeText) {
                navigator.clipboard.writeText(text).then(() => {
                    if (window.GMapsApp) window.GMapsApp.toast("Suggestion copied.", "success");
                });
            }
        });
        output.appendChild(copy);
    }

    function renderMessage(message, tone) {
        output.replaceChildren();
        const notice = document.createElement("div");
        notice.className = "notice notice-" + (tone || "info");
        const body = document.createElement("div");
        const text = document.createElement("p");
        text.textContent = message;
        body.appendChild(text);
        notice.appendChild(body);
        output.appendChild(notice);
    }

    async function run() {
        const request = input.value.trim();
        if (!request) {
            renderMessage("Describe what you want the local model to work on.", "warning");
            return;
        }
        let structured;
        const raw = context.value.trim();
        if (raw) {
            try {
                structured = JSON.parse(raw);
            } catch (error) {
                renderMessage("The structured context is not valid JSON.", "error");
                return;
            }
        }
        console_.setAttribute("aria-busy", "true");
        runButton.disabled = true;
        renderMessage("Waiting for the local model…", "info");
        try {
            const body = { task: taskPicker.value, input: request };
            if (structured !== undefined) body.context = structured;
            const response = await fetch("/api/v1/ai/assist", {
                method: "POST",
                credentials: "same-origin",
                headers: { "Accept": "application/json", "Content-Type": "application/json", "X-CSRF-Token": csrf },
                body: JSON.stringify(body)
            });
            const payload = await response.json().catch(() => ({}));
            if (!response.ok) throw new Error((payload.error && payload.error.message) || "The local model did not answer");
            renderResult(payload.data || {});
        } catch (error) {
            renderMessage(error.message || "The local model did not answer.", "error");
        } finally {
            console_.removeAttribute("aria-busy");
            runButton.disabled = false;
        }
    }

    async function probe() {
        if (console_.dataset.aiEnabled !== "true") return;
        try {
            const response = await fetch("/api/v1/ai/status", {
                credentials: "same-origin",
                headers: { "Accept": "application/json" }
            });
            if (!response.ok) return;
            const payload = await response.json().catch(() => ({}));
            const status = payload.data || {};
            if (!status.enabled || !status.reachable) return;
            console_.hidden = false;
            setState(status.model ? "Ready · " + status.model : "Ready", "success");
        } catch (error) {
            // A missing or stopped Ollama server is a normal local state, not a
            // failure to report: the console simply stays hidden.
        }
    }

    taskPicker.addEventListener("change", syncTask);
    runButton.addEventListener("click", run);
    clearButton.addEventListener("click", () => {
        input.value = "";
        context.value = "";
        renderMessage("Answers appear here. Review every suggestion before acting on it.", "info");
    });

    syncTask();
    probe();
}());
