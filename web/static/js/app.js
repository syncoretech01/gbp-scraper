(function () {
    "use strict";

    const root = document.documentElement;
    const shell = document.querySelector("[data-app-shell]");
    const palette = document.getElementById("command-palette");
    const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)");

    function motionIsReduced() {
        return reducedMotion.matches || root.dataset.reducedMotion === "true";
    }

    function safeStorageGet(key) {
        try { return window.localStorage.getItem(key); } catch (_) { return null; }
    }

    function safeStorageSet(key, value) {
        try { window.localStorage.setItem(key, value); } catch (_) { /* local storage may be disabled */ }
    }

    function applyTheme(theme) {
        const allowed = ["light", "dark", "system"];
        const value = allowed.includes(theme) ? theme : "system";
        root.dataset.theme = value;
        safeStorageSet("gmaps-theme", value);
        document.querySelectorAll("[data-theme-icon]").forEach((icon) => {
            icon.textContent = value === "light" ? "☀" : value === "dark" ? "☾" : "◐";
        });
    }

    function cycleTheme() {
        const order = ["system", "light", "dark"];
        const current = root.dataset.theme || "system";
        applyTheme(order[(order.indexOf(current) + 1) % order.length]);
        toast("Appearance set to " + root.dataset.theme + ".");
    }

    function toggleSidebar() {
        if (!shell) return;
        if (window.matchMedia("(max-width: 56rem)").matches) {
            const open = shell.dataset.mobileNav !== "open";
            shell.dataset.mobileNav = open ? "open" : "closed";
            document.querySelectorAll('[data-action="toggle-sidebar"]').forEach((button) => button.setAttribute("aria-expanded", String(open)));
            return;
        }
        const collapsed = shell.dataset.sidebar !== "collapsed";
        shell.dataset.sidebar = collapsed ? "collapsed" : "expanded";
        safeStorageSet("gmaps-sidebar", shell.dataset.sidebar);
        document.querySelectorAll('[data-action="toggle-sidebar"]').forEach((button) => button.setAttribute("aria-expanded", String(!collapsed)));
    }

    function toast(message, level) {
        const region = document.querySelector("[data-toast-region]");
        if (!region || !message) return;
        const item = document.createElement("div");
        item.className = "toast" + (level ? " notice-" + level : "");
        // Failures interrupt; successes do not. The region itself is polite,
        // so an error carries its own assertive role.
        if (level === "error") item.setAttribute("role", "alert");
        item.textContent = message;
        region.appendChild(item);
        window.setTimeout(() => item.remove(), motionIsReduced() ? 1000 : 5000);
    }

    function focusGlobalSearch() {
        const search = document.getElementById("global-search");
        if (search) { search.focus(); search.select(); }
    }

    function openPalette() {
        if (!palette) return;
        if (typeof palette.showModal === "function") palette.showModal();
        else palette.setAttribute("open", "");
        const input = palette.querySelector("[data-command-query]");
        if (input) window.setTimeout(() => { input.value = ""; input.dispatchEvent(new Event("input")); input.focus(); }, 0);
    }

    function closePalette() {
        if (!palette) return;
        if (typeof palette.close === "function") palette.close();
        else palette.removeAttribute("open");
    }

    function openDialog(selector) {
        const dialog = selector ? document.querySelector(selector) : null;
        if (!dialog) return;
        if (typeof dialog.showModal === "function") dialog.showModal();
        else dialog.setAttribute("open", "");
    }

    function closeDialog(selector, trigger) {
        const dialog = selector ? document.querySelector(selector) : trigger && trigger.closest("dialog");
        if (!dialog) return;
        if (typeof dialog.close === "function") dialog.close();
        else dialog.removeAttribute("open");
    }

    function copyValue(trigger) {
        const selector = trigger.dataset.copyTarget;
        const target = selector ? document.querySelector(selector) : null;
        const value = target ? (target.value || target.textContent || "") : trigger.dataset.copyValue;
        if (!value || !navigator.clipboard) return;
        navigator.clipboard.writeText(value).then(() => toast("Copied to clipboard.", "success"), () => toast("Could not copy the value.", "error"));
    }

    function selectTab(trigger) {
        const tablist = trigger.closest('[role="tablist"]');
        if (!tablist) return;
        tablist.querySelectorAll('[role="tab"]').forEach((tab) => {
            const selected = tab === trigger;
            tab.setAttribute("aria-selected", String(selected));
            tab.tabIndex = selected ? 0 : -1;
            const panel = document.getElementById(tab.getAttribute("aria-controls"));
            if (panel) panel.hidden = !selected;
        });
    }

    async function enhanceForm(form, submitControl) {
        // event.submitter names the button that actually submitted; only fall
        // back to activeElement for browsers that do not provide it.
        const submitter = submitControl || document.activeElement;
        const endpoint = (submitter && submitter.dataset.endpoint) || form.dataset.endpoint || form.action;
        if (!endpoint) return;
        const body = new FormData(form);
        const method = (submitter && submitter.dataset.method) || form.method || "POST";
        const csrf = form.querySelector('[name="csrf_token"]');
        const headers = { "Accept": "application/json" };
        if (csrf && csrf.value) headers["X-CSRF-Token"] = csrf.value;

        form.setAttribute("aria-busy", "true");
        try {
            const response = await fetch(endpoint, { method: method.toUpperCase(), body, headers, credentials: "same-origin" });
            const payload = await response.json().catch(() => ({}));
            if (!response.ok) throw new Error(payload.message || payload.error || "Request failed with status " + response.status);
            toast(payload.message || form.dataset.success || "Saved.", "success");
            document.dispatchEvent(new CustomEvent("app:form-success", { detail: { form, payload } }));
            if (payload.redirect) window.location.assign(payload.redirect);
            // Endpoints that answer with a bare 200 leave the page showing
            // stale rows; data-reload asks for a refresh once the toast fires.
            else if (form.dataset.reload === "true") window.setTimeout(() => window.location.reload(), 400);
        } catch (error) {
            toast(error.message || "The request could not be completed.", "error");
            const errors = form.querySelector("[data-form-errors]");
            if (errors) { errors.hidden = false; errors.textContent = error.message; errors.focus(); }
        } finally {
            form.removeAttribute("aria-busy");
        }
    }

    let searchController;
    let searchTimer;
    function setupGlobalSearch() {
        const form = document.querySelector(".global-search");
        const input = form && form.querySelector("input");
        const results = form && form.querySelector("[data-search-results]");
        if (!form || !input || !results) return;

        input.addEventListener("input", () => {
            window.clearTimeout(searchTimer);
            const query = input.value.trim();
            if (query.length < 2) { results.hidden = true; input.setAttribute("aria-expanded", "false"); return; }
            searchTimer = window.setTimeout(async () => {
                if (searchController) searchController.abort();
                searchController = new AbortController();
                try {
                    const endpoint = new URL(form.dataset.endpoint || "/api/v1/search", window.location.origin);
                    endpoint.searchParams.set("q", query);
                    endpoint.searchParams.set("limit", "8");
                    const response = await fetch(endpoint, { headers: { Accept: "application/json" }, signal: searchController.signal });
                    if (!response.ok) {
                        results.textContent = "Search is unavailable right now (HTTP " + response.status + ").";
                        results.hidden = false;
                        input.setAttribute("aria-expanded", "true");
                        return;
                    }
                    const payload = await response.json();
                    const items = payload.items || (payload.data && payload.data.items) || [];
                    results.replaceChildren();
                    items.forEach((item) => {
                        const link = document.createElement("a");
                        link.href = item.url;
                        link.dataset.action = "navigate";
                        link.dataset.endpoint = item.url;
                        link.textContent = item.type + " · " + item.title;
                        results.appendChild(link);
                    });
                    if (!items.length) results.textContent = "No matching local records.";
                    results.hidden = false;
                    input.setAttribute("aria-expanded", "true");
                } catch (error) {
                    if (error.name === "AbortError") return;
                    results.textContent = "Search could not reach the local workspace.";
                    results.hidden = false;
                    input.setAttribute("aria-expanded", "true");
                }
            }, 180);
        });
        input.addEventListener("blur", () => window.setTimeout(() => { results.hidden = true; input.setAttribute("aria-expanded", "false"); }, 150));
    }

    function paletteItems() {
        return palette ? Array.from(palette.querySelectorAll("[data-command]")).filter((item) => !item.hidden) : [];
    }

    function highlightPaletteItem(items, index) {
        items.forEach((item, position) => {
            if (position === index) {
                item.dataset.active = "true";
                item.scrollIntoView({ block: "nearest" });
            } else delete item.dataset.active;
        });
    }

    function setupCommandFilter() {
        const input = palette && palette.querySelector("[data-command-query]");
        if (!input) return;
        const empty = palette.querySelector("[data-command-empty]");
        input.addEventListener("input", () => {
            const query = input.value.trim().toLowerCase();
            palette.querySelectorAll("[data-command]").forEach((item) => {
                item.hidden = Boolean(query) && !item.dataset.command.includes(query) && !item.textContent.toLowerCase().includes(query);
                delete item.dataset.active;
            });
            const items = paletteItems();
            if (empty) empty.hidden = items.length > 0;
            highlightPaletteItem(items, 0);
        });
        input.addEventListener("keydown", (event) => {
            const items = paletteItems();
            if (!items.length) return;
            const active = items.findIndex((item) => item.dataset.active === "true");
            if (event.key === "ArrowDown") {
                event.preventDefault();
                highlightPaletteItem(items, active >= items.length - 1 ? 0 : active + 1);
            } else if (event.key === "ArrowUp") {
                event.preventDefault();
                highlightPaletteItem(items, active <= 0 ? items.length - 1 : active - 1);
            } else if (event.key === "Enter") {
                event.preventDefault();
                const target = items[active >= 0 ? active : 0];
                if (target) target.click();
                closePalette();
            }
        });
    }

    function applyDisplayFormatting() {
        const locale = root.lang || "en";
        const dateMode = root.dataset.dateTimeFormat || "local";
        document.querySelectorAll("time[datetime]").forEach((element) => {
            const date = new Date(element.dateTime);
            if (Number.isNaN(date.getTime())) return;
            if (dateMode === "iso") element.textContent = date.toISOString();
            else {
                const formatLocale = dateMode === "us" ? "en-US" : dateMode === "eu" ? "en-GB" : locale;
                try { element.textContent = new Intl.DateTimeFormat(formatLocale, { dateStyle: "medium", timeStyle: "short" }).format(date); }
                catch (_) { element.textContent = date.toLocaleString(); }
            }
        });
        if (root.dataset.numberFormat !== "plain") {
            document.querySelectorAll("[data-number]").forEach((element) => {
                const value = Number(element.dataset.number);
                if (!Number.isFinite(value)) return;
                try { element.textContent = new Intl.NumberFormat(locale).format(value); }
                catch (_) { element.textContent = String(value); }
            });
        }
    }

    document.addEventListener("click", (event) => {
        const trigger = event.target.closest("[data-action]");
        if (!trigger) return;
        const action = trigger.dataset.action;
        const inPalette = Boolean(palette && palette.open && palette.contains(trigger));
        if (action === "toggle-sidebar") { event.preventDefault(); toggleSidebar(); if (inPalette) closePalette(); }
        else if (action === "cycle-theme") { event.preventDefault(); cycleTheme(); if (inPalette) closePalette(); }
        else if (action === "focus-global-search") { event.preventDefault(); closePalette(); window.setTimeout(focusGlobalSearch, 0); }
        else if (action === "open-command-palette") { event.preventDefault(); openPalette(); }
        else if (action === "close-command-palette") { closePalette(); }
        else if (action === "open-dialog") { event.preventDefault(); openDialog(trigger.dataset.target); }
        else if (action === "close-dialog") { event.preventDefault(); closeDialog(trigger.dataset.target, trigger); }
        else if (action === "copy") { event.preventDefault(); copyValue(trigger); }
        else if (action === "select-tab") { event.preventDefault(); selectTab(trigger); }
    });

    document.addEventListener("submit", (event) => {
        const form = event.target;
        if (!(form instanceof HTMLFormElement)) return;
        if (form.dataset.confirm && !window.confirm(form.dataset.confirm)) { event.preventDefault(); return; }
        if (form.dataset.enhance === "json") { event.preventDefault(); enhanceForm(form, event.submitter); return; }
        // Plain forms navigate away. Mark the form busy on the next tick so
        // the submitter's name/value is still serialized, giving the operator
        // in-flight feedback instead of an apparently inert button.
        if (form.method && form.method.toLowerCase() === "post" && !form.target) {
            window.setTimeout(() => form.setAttribute("aria-busy", "true"), 0);
        }
    });

    document.addEventListener("keydown", (event) => {
        const typing = /^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement && document.activeElement.tagName);
        if (event.key === "Escape") {
            if (palette && palette.open) closePalette();
            // Native modal dialogs close themselves; the attribute fallback
            // used when showModal is unavailable needs an explicit close.
            document.querySelectorAll("dialog[open]").forEach((dialog) => {
                if (typeof dialog.close === "function") dialog.close();
                else dialog.removeAttribute("open");
            });
            if (shell) shell.dataset.mobileNav = "closed";
            return;
        }
        if ((event.ctrlKey || event.metaKey) && !event.altKey && event.key.toLowerCase() === "k") {
            event.preventDefault();
            if (palette && palette.open) closePalette();
            else openPalette();
            return;
        }
        if ((event.ctrlKey || event.metaKey) && !event.altKey && event.key.toLowerCase() === "e") {
            event.preventDefault();
            window.location.assign("/app/exports");
            return;
        }
        if (typing || event.ctrlKey || event.metaKey || event.altKey) return;
        if (event.key === "/") { event.preventDefault(); focusGlobalSearch(); }
        else if (event.key.toLowerCase() === "n") window.location.assign("/app/scrapes/new");
        else if (event.key.toLowerCase() === "j") window.location.assign("/app/jobs");
        else if (event.key.toLowerCase() === "r") window.location.assign("/app/results");
        else if (event.key.toLowerCase() === "p") {
            const pause = document.querySelector('[data-action="pause-job"]');
            if (pause) { event.preventDefault(); pause.click(); }
        }
    });

    applyTheme(safeStorageGet("gmaps-theme") || root.dataset.theme || "system");
    const savedSidebar = safeStorageGet("gmaps-sidebar");
    const defaultSidebar = root.dataset.sidebarDefault === "collapsed" ? "collapsed" : "expanded";
    if (shell && (savedSidebar || defaultSidebar) === "collapsed" && !window.matchMedia("(max-width: 56rem)").matches) shell.dataset.sidebar = "collapsed";
    setupGlobalSearch();
    setupCommandFilter();
    applyDisplayFormatting();
    document.querySelectorAll("[data-flash]").forEach((item) => toast(item.dataset.flash, item.classList.contains("notice-error") ? "error" : "success"));

    // Restoring from the back/forward cache replays the DOM as it was left,
    // including any in-flight busy state. Clear it so controls stay usable.
    window.addEventListener("pageshow", () => {
        document.querySelectorAll('form[aria-busy="true"]').forEach((form) => form.removeAttribute("aria-busy"));
    });

    window.GMapsApp = { toast, openDialog, closeDialog };
})();
