(function () {
    "use strict";

    const explorer = document.querySelector("[data-results-explorer]");
    if (!explorer) return;

    const currentLayoutKey = "gmaps-results-layout-v1";
    const namedLayoutsKey = "gmaps-results-layouts-v1";
    const maximumNamedLayouts = 12;
    const maximumStorageBytes = 64 * 1024;
    const maximumClipboardBytes = 1024 * 1024;
    const maximumFrozenColumns = 4;
    const table = explorer.querySelector(".results-table");
    const tableBody = table && table.tBodies[0];
    const tablePane = explorer.querySelector("[data-results-table-pane]");
    const mapPane = explorer.querySelector("[data-results-map-pane]");
    const mapFrame = explorer.querySelector("[data-results-map-frame]");
    const workspaceView = explorer.querySelector("[data-results-workspace-view]");
    const pagination = explorer.querySelector("[data-results-pagination]");
    const layoutDialog = document.getElementById("results-layout-dialog");
    const layoutSelect = explorer.querySelector("[data-layout-select]");
    const layoutState = explorer.querySelector("[data-layout-state]");
    const statusRegion = explorer.querySelector("[data-results-status]");
    const count = explorer.querySelector("[data-selection-count]");
    const bar = explorer.querySelector("[data-selection-bar]");
    const checkboxes = () => Array.from(explorer.querySelectorAll('[name="result_ids"]'));
    const resultRows = () => Array.from(explorer.querySelectorAll("[data-result-row]"));
    const selectedRows = () => resultRows().filter((row) => {
        const checkbox = row.querySelector('[name="result_ids"]');
        return checkbox && checkbox.checked;
    });

    const columnDefinitions = table ? Array.from(table.querySelectorAll("thead [data-column]")).map((header) => ({
        key: header.dataset.column,
        label: header.dataset.columnLabel || header.textContent.trim()
    })) : [];
    const knownColumnKeys = columnDefinitions.map((column) => column.key);
    const defaultLayout = {
        order: knownColumnKeys.slice(),
        visible: knownColumnKeys.slice(),
        frozen: [],
        widths: {},
        density: "comfortable",
        group: "none",
        mode: "table"
    };
    let layout = normalizeLayout(readStoredJSON(currentLayoutKey));
    let activeLayoutName = "";

    resultRows().forEach((row, index) => { row.dataset.originalIndex = String(index); });

    function announce(message, level) {
        if (statusRegion) statusRegion.textContent = message;
        if (window.GMapsApp && message) window.GMapsApp.toast(message, level || "success");
    }

    function readStorage(key) {
        try {
            const value = window.localStorage.getItem(key);
            return value && value.length <= maximumStorageBytes ? value : "";
        } catch (_) {
            return "";
        }
    }

    function writeStorage(key, value) {
        if (typeof value !== "string" || value.length > maximumStorageBytes) return false;
        try {
            window.localStorage.setItem(key, value);
            return true;
        } catch (_) {
            return false;
        }
    }

    function readStoredJSON(key) {
        const value = readStorage(key);
        if (!value) return null;
        try {
            const parsed = JSON.parse(value);
            return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed : null;
        } catch (_) {
            return null;
        }
    }

    function normalizeLayout(candidate) {
        const input = candidate && typeof candidate === "object" && !Array.isArray(candidate) ? candidate : {};
        const requestedOrder = Array.isArray(input.order) ? input.order : [];
        const order = [];
        requestedOrder.concat(knownColumnKeys).forEach((key) => {
            if (knownColumnKeys.includes(key) && !order.includes(key)) order.push(key);
        });
        if (order.includes("select")) {
            order.splice(order.indexOf("select"), 1);
            order.unshift("select");
        }

        const requestedVisible = Array.isArray(input.visible) ? input.visible : defaultLayout.visible;
        const visible = order.filter((key) => requestedVisible.includes(key));
        ["select", "name"].forEach((required) => {
            if (order.includes(required) && !visible.includes(required)) visible.push(required);
        });
        const frozen = Array.isArray(input.frozen) ? input.frozen.filter((key) =>
            visible.includes(key) && key !== "select" && key !== "actions"
        ).slice(0, maximumFrozenColumns) : [];
        const widths = {};
        if (input.widths && typeof input.widths === "object" && !Array.isArray(input.widths)) {
            Object.keys(input.widths).forEach((key) => {
                const width = Number(input.widths[key]);
                if (knownColumnKeys.includes(key) && Number.isFinite(width)) widths[key] = Math.max(72, Math.min(640, Math.round(width)));
            });
        }
        const density = ["compact", "comfortable", "spacious"].includes(input.density) ? input.density : "comfortable";
        const group = ["none", "category", "city", "status", "reviewed"].includes(input.group) ? input.group : "none";
        const modeAllowed = mapPane ? ["table", "map", "split"] : ["table"];
        const mode = modeAllowed.includes(input.mode) ? input.mode : "table";
        return { order, visible, frozen, widths, density, group, mode };
    }

    function serializeLayout(value) {
        return {
            order: value.order.slice(),
            visible: value.visible.slice(),
            frozen: value.frozen.slice(),
            widths: Object.assign({}, value.widths),
            density: value.density,
            group: value.group,
            mode: value.mode
        };
    }

    function persistCurrentLayout() {
        return writeStorage(currentLayoutKey, JSON.stringify(serializeLayout(layout)));
    }

    function readNamedLayouts() {
        const stored = readStoredJSON(namedLayoutsKey);
        const items = stored && Array.isArray(stored.layouts) ? stored.layouts : [];
        const names = new Set();
        const safe = [];
        items.slice(0, maximumNamedLayouts * 2).forEach((item) => {
            if (!item || typeof item !== "object") return;
            const name = cleanLayoutName(item.name);
            const key = name.toLocaleLowerCase();
            if (!name || names.has(key) || safe.length >= maximumNamedLayouts) return;
            names.add(key);
            safe.push({ name, layout: normalizeLayout(item.layout) });
        });
        return safe;
    }

    function cleanLayoutName(value) {
        return String(value || "").replace(/[\u0000-\u001f\u007f]/g, "").replace(/\s+/g, " ").trim().slice(0, 48);
    }

    function saveNamedLayout() {
        const input = layoutDialog && layoutDialog.querySelector("[data-layout-name]");
        const name = cleanLayoutName(input && input.value);
        if (!name) {
            announce("Enter a layout name of up to 48 characters.", "error");
            if (input) input.focus();
            return;
        }
        const items = readNamedLayouts();
        const existing = items.findIndex((item) => item.name.toLocaleLowerCase() === name.toLocaleLowerCase());
        const record = { name, layout: serializeLayout(layout) };
        if (existing >= 0) items[existing] = record;
        else if (items.length < maximumNamedLayouts) items.push(record);
        else {
            announce("Delete a saved layout before adding another (maximum 12).", "error");
            return;
        }
        if (!writeStorage(namedLayoutsKey, JSON.stringify({ version: 1, layouts: items }))) {
            announce("This browser could not save the table layout.", "error");
            return;
        }
        activeLayoutName = name;
        populateLayoutSelect();
        if (layoutSelect) layoutSelect.value = name;
        updateLayoutState();
        announce("Saved table layout “" + name + "”.");
    }

    function populateLayoutSelect() {
        if (!layoutSelect) return;
        const selected = activeLayoutName || layoutSelect.value;
        layoutSelect.replaceChildren();
        const current = document.createElement("option");
        current.value = "";
        current.textContent = "Current layout";
        layoutSelect.appendChild(current);
        readNamedLayouts().forEach((item) => {
            const option = document.createElement("option");
            option.value = item.name;
            option.textContent = item.name;
            layoutSelect.appendChild(option);
        });
        if (Array.from(layoutSelect.options).some((option) => option.value === selected)) layoutSelect.value = selected;
    }

    function loadNamedLayout() {
        const name = cleanLayoutName(layoutSelect && layoutSelect.value);
        if (!name) {
            announce("Choose a saved table layout first.", "error");
            return;
        }
        const item = readNamedLayouts().find((candidate) => candidate.name === name);
        if (!item) {
            populateLayoutSelect();
            announce("That saved layout is no longer available.", "error");
            return;
        }
        activeLayoutName = item.name;
        applyLayout(item.layout, true);
        announce("Loaded table layout “" + item.name + "”.");
    }

    function deleteNamedLayout() {
        const name = cleanLayoutName(layoutSelect && layoutSelect.value);
        if (!name) {
            announce("Choose a saved table layout first.", "error");
            return;
        }
        const items = readNamedLayouts().filter((item) => item.name !== name);
        if (!writeStorage(namedLayoutsKey, JSON.stringify({ version: 1, layouts: items }))) {
            announce("This browser could not update saved layouts.", "error");
            return;
        }
        if (activeLayoutName === name) activeLayoutName = "";
        populateLayoutSelect();
        updateLayoutState();
        announce("Deleted table layout “" + name + "”.");
    }

    function updateLayoutState() {
        if (layoutState) layoutState.textContent = activeLayoutName ? "Layout: " + activeLayoutName : "Current layout";
    }

    function headerFor(key) {
        return table && Array.from(table.querySelectorAll("thead [data-column]")).find((cell) => cell.dataset.column === key);
    }

    function cellFor(row, key) {
        return Array.from(row.children).find((cell) => cell.dataset && cell.dataset.column === key);
    }

    function reorderColumns() {
        if (!table) return;
        const headerRow = table.tHead && table.tHead.rows[0];
        if (headerRow) layout.order.forEach((key) => {
            const header = headerFor(key);
            if (header) headerRow.appendChild(header);
        });
        resultRows().forEach((row) => layout.order.forEach((key) => {
            const cell = cellFor(row, key);
            if (cell) row.appendChild(cell);
        }));
    }

    function applyColumnVisibilityAndWidths() {
        if (!table) return;
        layout.order.forEach((key) => {
            const visible = layout.visible.includes(key);
            const width = layout.widths[key];
            const cells = [headerFor(key)].concat(resultRows().map((row) => cellFor(row, key))).filter(Boolean);
            cells.forEach((cell) => {
                cell.hidden = !visible;
                cell.style.width = width ? width + "px" : "";
                cell.style.minWidth = width ? width + "px" : "";
                cell.style.maxWidth = width ? width + "px" : "";
            });
        });
    }

    function applyFrozenColumns() {
        if (!table) return;
        table.querySelectorAll("[data-column]").forEach((cell) => {
            delete cell.dataset.frozen;
            cell.style.left = "";
        });
        let left = 0;
        layout.order.filter((key) => layout.visible.includes(key)).forEach((key) => {
            const frozen = key === "select" || layout.frozen.includes(key);
            if (!frozen) return;
            const header = headerFor(key);
            const cells = [header].concat(resultRows().map((row) => cellFor(row, key))).filter(Boolean);
            cells.forEach((cell) => {
                cell.dataset.frozen = "true";
                cell.style.left = left + "px";
            });
            if (header) left += Math.ceil(header.getBoundingClientRect().width);
        });
    }

    function groupRows() {
        if (!tableBody) return;
        tableBody.querySelectorAll("[data-result-group]").forEach((row) => row.remove());
        const rows = resultRows().sort((left, right) => Number(left.dataset.originalIndex) - Number(right.dataset.originalIndex));
        if (layout.group === "none") {
            rows.forEach((row) => tableBody.appendChild(row));
            return;
        }
        const groups = new Map();
        rows.forEach((row) => {
            const dataName = "group" + layout.group.charAt(0).toUpperCase() + layout.group.slice(1);
            const label = String(row.dataset[dataName] || "").trim() || "Not specified";
            if (!groups.has(label)) groups.set(label, []);
            groups.get(label).push(row);
        });
        groups.forEach((groupRowsForLabel, label) => {
            const heading = document.createElement("tr");
            heading.dataset.resultGroup = "true";
            heading.className = "results-group-row";
            const cell = document.createElement("td");
            cell.colSpan = Math.max(1, layout.visible.length);
            cell.setAttribute("role", "rowheader");
            const strong = document.createElement("strong");
            strong.textContent = label;
            const suffix = document.createElement("span");
            suffix.textContent = " (" + groupRowsForLabel.length + ")";
            cell.append(strong, suffix);
            heading.appendChild(cell);
            tableBody.appendChild(heading);
            groupRowsForLabel.forEach((row) => tableBody.appendChild(row));
        });
    }

    function setViewMode(mode) {
        const next = mapPane && ["table", "map", "split"].includes(mode) ? mode : "table";
        layout.mode = next;
        if (workspaceView) workspaceView.dataset.view = next;
        if (tablePane) tablePane.hidden = next === "map";
        if (mapPane) mapPane.hidden = next === "table";
        if (pagination) pagination.hidden = next === "map";
        explorer.querySelectorAll("[data-results-mode]").forEach((input) => { input.checked = input.value === next; });
        if (mapFrame && next !== "table" && !mapFrame.src) mapFrame.src = mapFrame.dataset.src;
    }

    function renderColumnControls() {
        const list = layoutDialog && layoutDialog.querySelector("[data-column-list]");
        if (!list) return;
        list.replaceChildren();
        layout.order.forEach((key, index) => {
            const definition = columnDefinitions.find((column) => column.key === key);
            if (!definition) return;
            const item = document.createElement("div");
            item.className = "results-column-control";
            item.dataset.columnControl = key;

            const visibleLabel = document.createElement("label");
            visibleLabel.className = "check-row";
            const visible = document.createElement("input");
            visible.type = "checkbox";
            visible.checked = layout.visible.includes(key);
            visible.disabled = key === "select" || key === "name";
            visible.dataset.columnVisible = key;
            const visibleText = document.createElement("span");
            visibleText.textContent = definition.label;
            visibleLabel.append(visible, visibleText);

            const frozenLabel = document.createElement("label");
            frozenLabel.className = "check-row";
            const frozen = document.createElement("input");
            frozen.type = "checkbox";
            frozen.checked = key === "select" || layout.frozen.includes(key);
            frozen.disabled = key === "select" || key === "actions" || !layout.visible.includes(key);
            frozen.dataset.columnFrozen = key;
            const frozenText = document.createElement("span");
            frozenText.textContent = "Freeze";
            frozenLabel.append(frozen, frozenText);

            const width = document.createElement("span");
            width.className = "muted";
            width.textContent = layout.widths[key] ? layout.widths[key] + " px" : "Auto width";

            const actions = document.createElement("div");
            actions.className = "table-actions";
            [["up", "Move left"], ["down", "Move right"]].forEach(([direction, label]) => {
                const button = document.createElement("button");
                button.className = "button button-small";
                button.type = "button";
                button.dataset.action = "move-column";
                button.dataset.column = key;
                button.dataset.direction = direction;
                button.textContent = direction === "up" ? "←" : "→";
                button.setAttribute("aria-label", label + " " + definition.label);
                button.disabled = key === "select" || (direction === "up" ? index <= (layout.order.includes("select") ? 1 : 0) : index === layout.order.length - 1);
                actions.appendChild(button);
            });
            const reset = document.createElement("button");
            reset.className = "button button-small";
            reset.type = "button";
            reset.dataset.action = "reset-column-width";
            reset.dataset.column = key;
            reset.textContent = "Auto";
            reset.disabled = !layout.widths[key];
            reset.setAttribute("aria-label", "Reset width for " + definition.label);
            actions.appendChild(reset);
            item.append(visibleLabel, frozenLabel, width, actions);
            list.appendChild(item);
        });
    }

    function applyLayout(candidate, persist) {
        layout = normalizeLayout(candidate);
        reorderColumns();
        applyColumnVisibilityAndWidths();
        groupRows();
        if (table) table.dataset.density = layout.density;
        explorer.querySelectorAll("[data-layout-density]").forEach((control) => { control.value = layout.density; });
        explorer.querySelectorAll("[data-layout-group]").forEach((control) => { control.value = layout.group; });
        setViewMode(layout.mode);
        renderColumnControls();
        window.requestAnimationFrame(applyFrozenColumns);
        if (persist) persistCurrentLayout();
        updateLayoutState();
        ensureFocusableCell();
    }

    function markLayoutChanged() {
        activeLayoutName = "";
        if (layoutSelect) layoutSelect.value = "";
        updateLayoutState();
        persistCurrentLayout();
    }

    function setupResizeHandles() {
        if (!table) return;
        columnDefinitions.forEach((definition) => {
            if (definition.key === "select") return;
            const header = headerFor(definition.key);
            if (!header || header.querySelector("[data-resize-column]")) return;
            const handle = document.createElement("button");
            handle.type = "button";
            handle.className = "column-resize-handle";
            handle.dataset.resizeColumn = definition.key;
            handle.setAttribute("aria-label", "Resize " + definition.label + " column");
            handle.addEventListener("pointerdown", (event) => startColumnResize(event, definition.key, header));
            handle.addEventListener("keydown", (event) => {
                if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
                event.preventDefault();
                const step = event.shiftKey ? 25 : 10;
                const current = layout.widths[definition.key] || Math.ceil(header.getBoundingClientRect().width);
                layout.widths[definition.key] = Math.max(72, Math.min(640, current + (event.key === "ArrowRight" ? step : -step)));
                applyColumnVisibilityAndWidths();
                applyFrozenColumns();
                renderColumnControls();
                markLayoutChanged();
            });
            header.appendChild(handle);
        });
    }

    function startColumnResize(event, key, header) {
        if (event.button !== 0) return;
        event.preventDefault();
        event.stopPropagation();
        const startX = event.clientX;
        const startWidth = Math.ceil(header.getBoundingClientRect().width);
        document.body.classList.add("is-resizing-column");
        const move = (moveEvent) => {
            const width = Math.max(72, Math.min(640, startWidth + moveEvent.clientX - startX));
            layout.widths[key] = Math.round(width);
            applyColumnVisibilityAndWidths();
            applyFrozenColumns();
        };
        const finish = () => {
            window.removeEventListener("pointermove", move);
            window.removeEventListener("pointerup", finish);
            window.removeEventListener("pointercancel", finish);
            document.body.classList.remove("is-resizing-column");
            renderColumnControls();
            markLayoutChanged();
        };
        window.addEventListener("pointermove", move);
        window.addEventListener("pointerup", finish, { once: true });
        window.addEventListener("pointercancel", finish, { once: true });
    }

    function updateFilterRow(row) {
        if (!row) return;
        const field = row.querySelector('[name="filter_field"]');
        const operator = row.querySelector('[name="filter_operator"]');
        const value = row.querySelector('[name="filter_value"]');
        if (!field || !operator || !value) return;
        const placeholders = {
            distance: "latitude,longitude,radius km",
            bbox: "min latitude,min longitude,max latitude,max longitude",
            polygon: '{"type":"Polygon","coordinates":[...]}',
            reviewed: "true or false",
            claimed: "true or false",
            social: "linkedin, facebook, instagram...",
            updated_at: "YYYY-MM-DD or start,end",
            first_seen_at: "YYYY-MM-DD or start,end",
            last_seen_at: "YYYY-MM-DD or start,end",
            scraped_at: "YYYY-MM-DD or start,end",
            last_checked_at: "YYYY-MM-DD or start,end"
        };
        value.placeholder = placeholders[field.value] || (operator.value === "between" ? "minimum,maximum" : "Value");
        const valueUnused = operator.value === "empty" || operator.value === "not_empty";
        value.readOnly = valueUnused;
        value.setAttribute("aria-disabled", String(valueUnused));
        if (valueUnused) value.value = "";
    }

    // Selection state is recomputed from a cached checkbox list and only the
    // rows that actually changed are touched. Rebuilding the table on every
    // click would thrash layout on a 250-row page for no benefit.
    const selectionCheckboxes = checkboxes();
    const selectAllBox = explorer.querySelector("[data-select-all]");
    const requiresSelection = Array.from(explorer.querySelectorAll("[data-requires-selection]"));

    function markRowSelected(checkbox) {
        const row = checkbox.closest("[data-result-row]");
        if (!row) return;
        const selected = String(checkbox.checked);
        if (row.getAttribute("aria-selected") !== selected) row.setAttribute("aria-selected", selected);
    }

    function updateSelection(changed) {
        if (changed) markRowSelected(changed);
        else selectionCheckboxes.forEach(markRowSelected);
        let selectionCount = 0;
        selectionCheckboxes.forEach((item) => { if (item.checked) selectionCount += 1; });
        if (count) count.textContent = String(selectionCount);
        if (bar) bar.hidden = selectionCount === 0;
        requiresSelection.forEach((control) => { control.disabled = selectionCount === 0; });
        if (selectAllBox) {
            selectAllBox.checked = selectionCount > 0 && selectionCount === selectionCheckboxes.length;
            selectAllBox.indeterminate = selectionCount > 0 && selectionCount < selectionCheckboxes.length;
        }
    }

    function clearSelection() {
        selectionCheckboxes.forEach((item) => {
            if (!item.checked) return;
            item.checked = false;
            markRowSelected(item);
        });
        updateSelection();
        announce("Selection cleared.");
    }

    // skeletonBlock and errorBlock give the drawer a designed loading and
    // failure appearance instead of leaving the previous record on screen.
    function skeletonBlock() {
        const wrapper = document.createElement("div");
        wrapper.className = "state-loading";
        wrapper.setAttribute("aria-hidden", "true");
        ["skeleton skeleton-text", "skeleton skeleton-text", "skeleton skeleton-block", "skeleton skeleton-block"].forEach((className) => {
            const bar = document.createElement("span");
            bar.className = className;
            wrapper.appendChild(bar);
        });
        return wrapper;
    }

    function errorBlock(message, fallbackURL) {
        const wrapper = document.createElement("div");
        wrapper.className = "state-error";
        wrapper.setAttribute("role", "alert");
        const inner = document.createElement("div");
        const title = document.createElement("strong");
        title.textContent = "This record could not be loaded";
        const detail = document.createElement("span");
        detail.textContent = message;
        inner.append(title, detail);
        if (fallbackURL) {
            const link = document.createElement("p");
            const anchor = document.createElement("a");
            anchor.className = "button button-small";
            anchor.href = fallbackURL;
            anchor.textContent = "Open the full record page";
            link.appendChild(anchor);
            inner.appendChild(link);
        }
        wrapper.appendChild(inner);
        return wrapper;
    }

    function clipboardText(value, description) {
        const text = String(value || "");
        if (!text) {
            announce("No " + description + " are available in the selected rows.", "error");
            return;
        }
        if (text.length > maximumClipboardBytes) {
            announce("The clipboard selection is too large. Export the rows instead.", "error");
            return;
        }
        const fallback = () => {
            const area = document.createElement("textarea");
            area.value = text;
            area.readOnly = true;
            area.className = "sr-only";
            document.body.appendChild(area);
            area.select();
            let copied = false;
            try { copied = document.execCommand("copy"); } catch (_) { copied = false; }
            area.remove();
            if (copied) announce("Copied " + description + ".");
            else announce("The browser could not copy this selection.", "error");
        };
        if (navigator.clipboard && typeof navigator.clipboard.writeText === "function") {
            navigator.clipboard.writeText(text).then(() => announce("Copied " + description + "."), fallback);
        } else fallback();
    }

    function copySelected(field) {
        const rows = selectedRows();
        if (!rows.length) return;
        if (field === "rows") {
            const columns = layout.order.filter((key) => layout.visible.includes(key) && key !== "select" && key !== "actions");
            const labels = columns.map((key) => {
                const definition = columnDefinitions.find((column) => column.key === key);
                return definition ? definition.label : key;
            });
            const lines = [labels.join("\t")];
            rows.forEach((row) => lines.push(columns.map((key) => {
                const cell = cellFor(row, key);
                return String(cell && (cell.dataset.copyValue || cell.textContent) || "").replace(/[\t\r\n]+/g, " ").trim();
            }).join("\t")));
            clipboardText(lines.join("\n"), "selected rows");
            return;
        }
        const values = [];
        rows.forEach((row) => {
            const value = String(row.dataset[field] || "").trim();
            if (value && !values.includes(value)) values.push(value);
        });
        const labels = { domain: "domains", email: "emails", phone: "phone numbers", address: "addresses", maps: "Maps URLs" };
        clipboardText(values.join("\n"), labels[field] || "values");
    }

    function exportSelected() {
        const ids = selectedRows().map((row) => row.dataset.businessId).filter(Boolean);
        if (!ids.length) return;
        const endpoint = new URL(explorer.dataset.exportUrl || "/app/exports", window.location.origin);
        endpoint.searchParams.set("source_scope", "selected");
        endpoint.searchParams.set("selected_ids", ids.join(","));
        endpoint.hash = "export-builder";
        if (endpoint.href.length > 16000) {
            announce("This selection is too large for a browser handoff. Export it from the Export Centre.", "error");
            return;
        }
        window.location.assign(endpoint.href);
    }

    function openSelectedWebsites() {
        const values = [];
        selectedRows().forEach((row) => {
            const raw = String(row.dataset.website || "").trim();
            if (!raw) return;
            try {
                const parsed = new URL(raw);
                if ((parsed.protocol === "http:" || parsed.protocol === "https:") && !values.includes(parsed.href)) values.push(parsed.href);
            } catch (_) { /* Invalid stored websites are deliberately skipped. */ }
        });
        if (!values.length) {
            announce("The selected rows do not contain valid HTTP websites.", "error");
            return;
        }
        const opened = values.slice(0, 20);
        opened.forEach((url) => {
            const child = window.open(url, "_blank", "noopener,noreferrer");
            if (child) child.opener = null;
        });
        const suffix = values.length > opened.length ? " The first 20 were opened; " + (values.length - opened.length) + " were skipped." : "";
        announce("Opened " + opened.length + " website" + (opened.length === 1 ? "" : "s") + "." + suffix);
    }

    function visibleCells(row) {
        return layout.order.map((key) => cellFor(row, key)).filter((cell) => cell && !cell.hidden);
    }

    function ensureFocusableCell() {
        if (!table) return;
        const cells = resultRows().flatMap(visibleCells);
        const current = cells.find((cell) => cell.tabIndex === 0);
        cells.forEach((cell) => { cell.tabIndex = cell === current ? 0 : -1; });
        if (!current && cells[0]) cells[0].tabIndex = 0;
    }

    function focusCell(cell) {
        if (!cell || cell.hidden) return;
        table.querySelectorAll("tbody td[tabindex]").forEach((candidate) => { candidate.tabIndex = candidate === cell ? 0 : -1; });
        cell.focus();
    }

    function handleTableKeydown(event) {
        if (!table) return;
        const cell = event.target.closest && event.target.closest("td[data-column]");
        const row = cell && cell.closest("[data-result-row]");
        if (!cell || !row) return;
        if ((event.ctrlKey || event.metaKey) && !event.altKey && event.key.toLowerCase() === "c") {
            event.preventDefault();
            clipboardText(cell.dataset.copyValue || cell.textContent.trim(), "cell");
            return;
        }
        const rows = resultRows();
        const rowIndex = rows.indexOf(row);
        const cells = visibleCells(row);
        const columnIndex = cells.indexOf(cell);
        let target = null;
        if (event.key === "ArrowLeft") target = cells[Math.max(0, columnIndex - 1)];
        else if (event.key === "ArrowRight") target = cells[Math.min(cells.length - 1, columnIndex + 1)];
        else if (event.key === "ArrowUp" && rowIndex > 0) target = cellFor(rows[rowIndex - 1], cell.dataset.column) || visibleCells(rows[rowIndex - 1])[0];
        else if (event.key === "ArrowDown" && rowIndex < rows.length - 1) target = cellFor(rows[rowIndex + 1], cell.dataset.column) || visibleCells(rows[rowIndex + 1])[0];
        else if (event.key === "Home") target = cells[0];
        else if (event.key === "End") target = cells[cells.length - 1];
        else if (event.key === " " && event.target === cell) {
            const checkbox = row.querySelector('[name="result_ids"]');
            if (checkbox) {
                event.preventDefault();
                checkbox.checked = !checkbox.checked;
                updateSelection(checkbox);
            }
            return;
        } else if (event.key === "Enter" && event.target === cell) {
            const link = cell.querySelector("a[href]");
            if (link) {
                event.preventDefault();
                link.click();
            }
            return;
        }
        if (target) {
            event.preventDefault();
            focusCell(target);
        }
    }

    async function updateReviewed(input) {
        const csrf = explorer.querySelector('[name="csrf_token"]');
        const row = input.closest("[data-result-row]");
        const previous = !input.checked;
        input.disabled = true;
        const body = new FormData();
        body.append("csrf_token", csrf ? csrf.value : "");
        body.append("result_ids", input.dataset.businessId || "");
        body.append("action", input.checked ? "reviewed" : "unreviewed");
        body.append("return_to", window.location.pathname + window.location.search);
        try {
            const response = await fetch("/api/v1/results/bulk", {
                method: "POST",
                body,
                credentials: "same-origin",
                headers: { Accept: "application/json", "X-CSRF-Token": csrf ? csrf.value : "" }
            });
            const payload = await response.json().catch(() => ({}));
            if (!response.ok) throw new Error(payload.message || payload.error || "Could not update review state.");
            if (row) {
                row.dataset.groupReviewed = input.checked ? "Reviewed" : "Needs review";
                const cell = cellFor(row, "workflow");
                if (cell) cell.dataset.copyValue = (input.checked ? "Reviewed" : "Needs review") + "; " + String(cell.dataset.copyValue || "").split("; ").slice(1).join("; ");
            }
            if (layout.group === "reviewed") groupRows();
            announce(input.checked ? "Marked reviewed." : "Marked as needing review.");
        } catch (error) {
            input.checked = previous;
            announce(error.message || "Could not update review state.", "error");
        } finally {
            input.disabled = false;
        }
    }

    // preclassifySelected queues the lightweight website pre-classification
    // profile for every selected business through the existing enrichment
    // endpoint. The server coerces {"preclassify":true} into the bounded
    // profile, so this stays a pure client-side action.
    async function preclassifySelected(trigger) {
        const ids = checkboxes().filter((item) => item.checked).map((item) => item.value).filter(Boolean);
        if (!ids.length) {
            announce("Select at least one business first.", "error");
            return;
        }
        const csrf = explorer.querySelector('[name="csrf_token"]');
        trigger.disabled = true;
        try {
            const response = await fetch("/api/v1/results/enrich", {
                method: "POST",
                credentials: "same-origin",
                headers: {
                    "Accept": "application/json",
                    "Content-Type": "application/json",
                    "X-CSRF-Token": csrf ? csrf.value : ""
                },
                body: JSON.stringify({ ids: ids, options: { preclassify: true } })
            });
            const payload = await response.json().catch(() => ({}));
            if (!response.ok) throw new Error((payload.error && payload.error.message) || payload.message || "Could not queue website pre-classification.");
            const meta = payload.meta || {};
            const queued = Number(meta.queued) || 0;
            const skipped = Number(meta.skipped) || 0;
            announce("Website pre-classification queued for " + queued + " business" + (queued === 1 ? "" : "es") +
                (skipped ? "; " + skipped + " skipped" : "") + ".");
        } catch (error) {
            announce(error.message || "Could not queue website pre-classification.", "error");
        } finally {
            trigger.disabled = false;
        }
    }

    explorer.addEventListener("change", (event) => {
        if (event.target.matches("[data-select-all]")) {
            selectionCheckboxes.forEach((item) => { item.checked = event.target.checked; });
            updateSelection();
        } else if (event.target.matches('[name="result_ids"]')) updateSelection(event.target);
        if (event.target.matches('[name="filter_field"], [name="filter_operator"]')) updateFilterRow(event.target.closest(".filter-row"));
        if (event.target.matches("[data-results-mode]")) {
            setViewMode(event.target.value);
            markLayoutChanged();
        }
        if (event.target.matches("[data-layout-density]")) {
            layout.density = event.target.value;
            applyLayout(layout, true);
            markLayoutChanged();
        }
        if (event.target.matches("[data-layout-group]")) {
            layout.group = event.target.value;
            applyLayout(layout, true);
            markLayoutChanged();
        }
        if (event.target.matches("[data-column-visible]")) {
            const key = event.target.dataset.columnVisible;
            if (event.target.checked && !layout.visible.includes(key)) layout.visible.push(key);
            if (!event.target.checked) layout.visible = layout.visible.filter((value) => value !== key);
            layout.frozen = layout.frozen.filter((value) => layout.visible.includes(value));
            applyLayout(layout, true);
            markLayoutChanged();
        }
        if (event.target.matches("[data-column-frozen]")) {
            const key = event.target.dataset.columnFrozen;
            if (event.target.checked && !layout.frozen.includes(key)) {
                if (layout.frozen.length >= maximumFrozenColumns) {
                    event.target.checked = false;
                    announce("At most four data columns can be frozen.", "error");
                    return;
                }
                layout.frozen.push(key);
            }
            if (!event.target.checked) layout.frozen = layout.frozen.filter((value) => value !== key);
            applyLayout(layout, true);
            markLayoutChanged();
        }
        if (event.target.matches("[data-inline-reviewed]")) updateReviewed(event.target);
    });

    explorer.addEventListener("click", async (event) => {
        const cell = event.target.closest("td[data-column]");
        if (cell && !event.target.closest("a, button, input, textarea, select, label")) focusCell(cell);
        const trigger = event.target.closest("[data-action]");
        if (!trigger) return;
        const action = trigger.dataset.action;
        if (action === "add-filter") {
            event.preventDefault();
            const template = document.getElementById("filter-row-template");
            const rows = explorer.querySelector("[data-filter-rows]");
            if (template && rows) {
                if (rows.children.length >= 25) {
                    announce("At most 25 simple filters can be added.", "error");
                    return;
                }
                rows.appendChild(template.content.cloneNode(true));
                updateFilterRow(rows.lastElementChild);
            }
        } else if (action === "remove-filter") {
            event.preventDefault();
            const row = trigger.closest(".filter-row");
            if (row) row.remove();
        } else if (action === "open-result") {
            if (!window.fetch || !trigger.dataset.endpoint) return;
            event.preventDefault();
            const drawer = document.getElementById("result-drawer");
            const body = drawer && drawer.querySelector("[data-drawer-body]");
            if (!drawer || !body) return;
            // Open first with a skeleton so the drawer never appears to hang,
            // and keep a designed error state inside it when the fetch fails.
            body.replaceChildren(skeletonBlock());
            body.setAttribute("aria-busy", "true");
            if (typeof drawer.showModal === "function") { if (!drawer.open) drawer.showModal(); }
            else drawer.setAttribute("open", "");
            try {
                const response = await fetch(trigger.dataset.endpoint, { headers: { Accept: "text/html" }, credentials: "same-origin" });
                if (!response.ok) throw new Error("Could not load this business (HTTP " + response.status + ").");
                body.innerHTML = await response.text();
            } catch (error) {
                body.replaceChildren(errorBlock(error.message || "Could not load this business.", trigger.getAttribute("href")));
                announce(error.message || "Could not load this business.", "error");
            } finally { body.removeAttribute("aria-busy"); }
        } else if (action === "clear-selection") {
            event.preventDefault();
            clearSelection();
        } else if (action === "copy-selected") {
            event.preventDefault();
            copySelected(trigger.dataset.field);
            const menu = trigger.closest("details");
            if (menu) menu.open = false;
        } else if (action === "open-selected-websites") {
            event.preventDefault();
            openSelectedWebsites();
        } else if (action === "preclassify-selected") {
            event.preventDefault();
            preclassifySelected(trigger);
        } else if (action === "export-selected") {
            event.preventDefault();
            exportSelected();
        } else if (action === "open-layout-dialog") {
            event.preventDefault();
            renderColumnControls();
            if (layoutDialog && typeof layoutDialog.showModal === "function") layoutDialog.showModal();
            else if (layoutDialog) layoutDialog.setAttribute("open", "");
        } else if (action === "close-layout-dialog") {
            if (layoutDialog && typeof layoutDialog.close === "function") layoutDialog.close();
            else if (layoutDialog) layoutDialog.removeAttribute("open");
        } else if (action === "save-layout") {
            saveNamedLayout();
        } else if (action === "load-layout") {
            loadNamedLayout();
        } else if (action === "delete-layout") {
            deleteNamedLayout();
        } else if (action === "reset-layout") {
            activeLayoutName = "";
            applyLayout(defaultLayout, true);
            if (layoutSelect) layoutSelect.value = "";
            announce("Restored the default table layout.");
        } else if (action === "move-column") {
            const key = trigger.dataset.column;
            const from = layout.order.indexOf(key);
            const offset = trigger.dataset.direction === "up" ? -1 : 1;
            const to = from + offset;
            const minimum = layout.order.includes("select") ? 1 : 0;
            if (from >= minimum && to >= minimum && to < layout.order.length) {
                layout.order.splice(from, 1);
                layout.order.splice(to, 0, key);
                applyLayout(layout, true);
                markLayoutChanged();
            }
        } else if (action === "reset-column-width") {
            delete layout.widths[trigger.dataset.column];
            applyLayout(layout, true);
            markLayoutChanged();
        }
    });

    if (table) {
        table.addEventListener("keydown", handleTableKeydown);
        table.addEventListener("focusin", (event) => {
            const cell = event.target.closest && event.target.closest("td[data-column]");
            if (cell) table.querySelectorAll("tbody td[tabindex]").forEach((candidate) => { candidate.tabIndex = candidate === cell ? 0 : -1; });
        });
    }

    const filterForm = explorer.querySelector('form[aria-labelledby="result-filter-title"]');
    if (filterForm) {
        filterForm.addEventListener("submit", (event) => {
            const input = filterForm.querySelector('[name="filter_json"]');
            if (!input || !input.value.trim()) return;
            try {
                const parsed = JSON.parse(input.value);
                if (!parsed || Array.isArray(parsed) || typeof parsed !== "object") throw new Error("Use one JSON object.");
                input.setCustomValidity("");
            } catch (error) {
                event.preventDefault();
                input.setCustomValidity("Enter a valid nested filter JSON object. " + error.message);
                input.reportValidity();
            }
        });
        const nested = filterForm.querySelector('[name="filter_json"]');
        if (nested) nested.addEventListener("input", () => nested.setCustomValidity(""));
    }

    // setupSortableHeaders turns headers that map onto a server-supported
    // sort key into real buttons. Sorting always round-trips through the
    // backend query so every page of results is ordered, not just this one.
    function setupSortableHeaders() {
        if (!table) return;
        const activeSort = new URLSearchParams(window.location.search).get("sort") || "updated_desc";
        table.querySelectorAll("thead th[data-sort-key]").forEach((header) => {
            const key = header.dataset.sortKey;
            const label = header.dataset.columnLabel || header.textContent.trim();
            const button = document.createElement("button");
            button.type = "button";
            button.className = "sort-button";
            button.textContent = label;
            button.setAttribute("aria-label", "Sort by " + label.toLowerCase() +
                (header.dataset.sortDirection === "ascending" ? " (A to Z)" : " (highest first)"));
            if (key === activeSort) header.setAttribute("aria-sort", header.dataset.sortDirection || "descending");
            button.addEventListener("click", () => {
                const target = new URL(window.location.href);
                target.searchParams.set("sort", key);
                target.searchParams.delete("page");
                window.location.assign(target.toString());
            });
            header.replaceChildren(button);
        });
    }

    explorer.querySelectorAll(".filter-row").forEach(updateFilterRow);
    populateLayoutSelect();
    setupSortableHeaders();
    setupResizeHandles();
    applyLayout(layout, false);
    updateSelection();
})();
