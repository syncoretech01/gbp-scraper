(function () {
    "use strict";

    const explorer = document.querySelector("[data-results-explorer]");
    if (!explorer) return;

    const currentLayoutKey = "gmaps-results-layout-v1";
    const namedLayoutsKey = "gmaps-results-layouts-v1";
    const columnProfileKey = "gmaps-results-profile-v1";
    const maximumNamedLayouts = 12;
    const maximumStorageBytes = 64 * 1024;
    const maximumClipboardBytes = 1024 * 1024;
    const maximumFrozenColumns = 4;
    const table = explorer.querySelector(".results-table");
    const tableBody = table && table.tBodies[0];
    // The scroll container is declared in the markup because row
    // virtualisation measures and listens on it rather than on the window.
    const tableWrap = explorer.querySelector("[data-results-virtual-scroll]") ||
        explorer.querySelector(".results-table-wrap");
    const tablePane = explorer.querySelector("[data-results-table-pane]");
    const mapPane = explorer.querySelector("[data-results-map-pane]");
    const mapFrame = explorer.querySelector("[data-results-map-frame]");
    const workspaceView = explorer.querySelector("[data-results-workspace-view]");
    const pagination = explorer.querySelector("[data-results-pagination]");
    const layoutDialog = document.getElementById("results-layout-dialog");
    const layoutSelect = explorer.querySelector("[data-layout-select]");
    const profileSelect = explorer.querySelector("[data-column-profile]");
    const layoutState = explorer.querySelector("[data-layout-state]");
    const statusRegion = explorer.querySelector("[data-results-status]");
    const count = explorer.querySelector("[data-selection-count]");
    const bar = explorer.querySelector("[data-selection-bar]");
    const rowSelectionSelector = 'input[type="checkbox"][name="result_ids"]';
    const checkboxes = () => Array.from(explorer.querySelectorAll(rowSelectionSelector));
    // orderedRows is the authoritative list of every business row on this page,
    // kept in visual order. Row virtualisation detaches the rows outside the
    // scroll window, so a DOM query would only ever see the window; everything
    // that means "the rows on this page" reads this list instead.
    let orderedRows = Array.from(explorer.querySelectorAll("[data-result-row]"));
    const resultRows = () => orderedRows;
    // selectionCheckboxes is snapshotted here, while every row is still in the
    // document, and then held by reference. A windowed-out checkbox keeps its
    // checked state while it is detached, so the selection survives scrolling.
    const selectionCheckboxes = checkboxes();
    const selectedRows = () => resultRows().filter((row) => {
        const checkbox = row.querySelector(rowSelectionSelector);
        return checkbox && checkbox.checked;
    });

    const columnDefinitions = table ? Array.from(table.querySelectorAll("thead [data-column]")).map((header) => ({
        key: header.dataset.column,
        label: header.dataset.columnLabel || header.textContent.trim()
    })) : [];
    const knownColumnKeys = columnDefinitions.map((column) => column.key);

    // Column profiles are named working sets over the existing column
    // machinery. "select", "name", and "actions" are implicit in every profile
    // because a row is unusable without them.
    const alwaysVisibleColumns = ["select", "name", "actions"];
    const columnProfiles = [
        { id: "prospecting", label: "Prospecting", columns: ["location", "website", "prospect", "tier", "score", "contacts"] },
        { id: "contact", label: "Contact", columns: ["category", "location", "contacts", "website", "workflow"] },
        { id: "quality", label: "Quality", columns: ["category", "rating", "reviews", "quality", "workflow", "source", "updated"] },
        { id: "geo", label: "Geography", columns: ["category", "location", "address", "source", "updated"] },
        { id: "everything", label: "Everything", columns: knownColumnKeys.slice() }
    ];
    const defaultProfileID = "prospecting";

    function profileColumns(id) {
        const profile = columnProfiles.find((candidate) => candidate.id === id);
        const requested = (profile ? profile.columns : []).concat(alwaysVisibleColumns);

        return knownColumnKeys.filter((key) => requested.includes(key));
    }

    const defaultLayout = {
        order: knownColumnKeys.slice(),
        visible: profileColumns(defaultProfileID),
        frozen: [],
        widths: {},
        density: "compact",
        group: "none",
        mode: "table"
    };
    // A saved view may carry its own visible columns and grouping. When the
    // URL supplies them they win over whatever this browser last stored, so
    // opening a shared view shows the table the view was saved with.
    function savedViewLayout() {
        const columns = String(explorer.dataset.viewColumns || "").split(",").filter(Boolean);
        const group = String(explorer.dataset.viewGroup || "").trim();
        if (!columns.length && !group) return null;
        const base = readStoredJSON(currentLayoutKey) || defaultLayout;
        const seeded = Object.assign({}, base);
        if (columns.length) {
            seeded.order = columns.concat(knownColumnKeys.filter((key) => !columns.includes(key)));
            seeded.visible = columns.slice();
        }
        if (group) seeded.group = group;
        return seeded;
    }

    let layout = normalizeLayout(savedViewLayout() || readStoredJSON(currentLayoutKey) || defaultLayout);
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
        const density = ["compact", "comfortable", "spacious"].includes(input.density) ? input.density : "compact";
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

    // syncSavedViewLayout keeps the save-as-view form carrying the layout the
    // operator can actually see, so a saved view stores filters, sorting,
    // visible columns, and grouping together.
    function syncSavedViewLayout() {
        const holder = explorer.querySelector("[data-save-view-columns]");
        const groupInput = explorer.querySelector("[data-save-view-group]");
        if (groupInput) groupInput.value = layout.group || "none";
        if (!holder) return;
        const visible = layout.order.filter((key) => layout.visible.includes(key));
        holder.replaceChildren();
        visible.forEach((key) => {
            const field = document.createElement("input");
            field.type = "hidden";
            field.name = "columns";
            field.value = key;
            holder.appendChild(field);
        });
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

    // --- Column profiles ---------------------------------------------------
    // A profile is only a named visible-column set; order, widths, frozen
    // columns, density, and grouping stay under the operator's control.

    function matchingProfileID() {
        const current = layout.visible.slice().sort().join(",");
        const match = columnProfiles.find((profile) => profileColumns(profile.id).slice().sort().join(",") === current);

        return match ? match.id : "";
    }

    function populateProfileSelect() {
        if (!profileSelect) return;
        profileSelect.replaceChildren();
        columnProfiles.forEach((profile) => {
            const option = document.createElement("option");
            option.value = profile.id;
            option.textContent = profile.label + " columns";
            profileSelect.appendChild(option);
        });
        const custom = document.createElement("option");
        custom.value = "";
        custom.textContent = "Custom columns";
        profileSelect.appendChild(custom);
        syncProfileSelect();
    }

    function syncProfileSelect() {
        if (profileSelect) profileSelect.value = matchingProfileID();
    }

    function applyColumnProfile(id) {
        const columns = profileColumns(id);
        if (!columns.length) return;
        layout.visible = columns;
        layout.frozen = layout.frozen.filter((key) => columns.includes(key));
        applyLayout(layout, true);
        markLayoutChanged();
        writeStorage(columnProfileKey, id);
        const profile = columnProfiles.find((candidate) => candidate.id === id);
        announce("Switched to the " + (profile ? profile.label.toLowerCase() : id) + " column profile.");
    }

    function restoreStoredProfile() {
        if (readStoredJSON(currentLayoutKey)) return;
        const stored = readStorage(columnProfileKey);
        const id = columnProfiles.some((profile) => profile.id === stored) ? stored : defaultProfileID;
        layout.visible = profileColumns(id);
    }

    function headerFor(key) {
        return table && Array.from(table.querySelectorAll("thead [data-column]")).find((cell) => cell.dataset.column === key);
    }

    // Cells are moved by reordering but never replaced, so each row's column
    // index is built once. Without it every layout pass costs one scan of the
    // row per column, which is what made a large page feel heavy.
    const rowColumnIndex = new WeakMap();

    function cellFor(row, key) {
        let index = rowColumnIndex.get(row);
        if (!index) {
            index = new Map();
            Array.from(row.children).forEach((cell) => {
                if (cell.dataset && cell.dataset.column) index.set(cell.dataset.column, cell);
            });
            rowColumnIndex.set(row, index);
        }

        return index.get(key);
    }

    function columnCells(key) {
        return [headerFor(key)].concat(resultRows().map((row) => cellFor(row, key))).filter(Boolean);
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
            const cells = columnCells(key);
            cells.forEach((cell) => {
                cell.hidden = !visible;
                cell.style.width = width ? width + "px" : "";
                cell.style.minWidth = width ? width + "px" : "";
                cell.style.maxWidth = width ? width + "px" : "";
            });
        });
    }

    // Frozen state is written onto every row on the page, windowed out or not,
    // so a row that scrolls back in is already pinned correctly. Only the
    // columns that stop being frozen are cleared: sweeping the whole table on
    // every pointer move during a resize is what a large page cannot afford.
    let appliedFrozenColumns = [];

    function applyFrozenColumns() {
        if (!table) return;
        const frozen = layout.order.filter((key) =>
            layout.visible.includes(key) && (key === "select" || layout.frozen.includes(key)));
        appliedFrozenColumns.forEach((key) => {
            if (frozen.includes(key)) return;
            columnCells(key).forEach((cell) => {
                delete cell.dataset.frozen;
                cell.style.left = "";
            });
        });
        let left = 0;
        frozen.forEach((key) => {
            const header = headerFor(key);
            columnCells(key).forEach((cell) => {
                cell.dataset.frozen = "true";
                cell.style.left = left + "px";
            });
            if (header) left += Math.ceil(header.getBoundingClientRect().width);
        });
        appliedFrozenColumns = frozen;
    }

    // groupRows rebuilds the row model — the visual order of the table, with
    // group heading rows interleaved when grouping is on — and then hands it to
    // the renderer. It never writes rows into the document itself; renderRows
    // is the only place that does, so grouping and virtualisation cannot
    // disagree about what is on screen.
    function groupRows() {
        if (!tableBody) return;
        const rows = resultRows().slice().sort((left, right) =>
            Number(left.dataset.originalIndex) - Number(right.dataset.originalIndex));
        const model = [];
        if (layout.group === "none") {
            rows.forEach((row) => model.push({ node: row, group: false }));
        } else {
            const groups = new Map();
            rows.forEach((row) => {
                const dataName = "group" + layout.group.charAt(0).toUpperCase() + layout.group.slice(1);
                const label = String(row.dataset[dataName] || "").trim() || "Not specified";
                if (!groups.has(label)) groups.set(label, []);
                groups.get(label).push(row);
            });
            groups.forEach((groupRowsForLabel, label) => {
                model.push({ node: groupHeadingRow(label, groupRowsForLabel.length), group: true });
                groupRowsForLabel.forEach((row) => model.push({ node: row, group: false }));
            });
        }
        rowModel = model;
        orderedRows = model.filter((entry) => !entry.group).map((entry) => entry.node);
        applyRowIndexes();
        renderRows(true);
    }

    function groupHeadingRow(label, size) {
        const heading = document.createElement("tr");
        heading.dataset.resultGroup = "true";
        heading.className = "results-group-row";
        const cell = document.createElement("td");
        cell.colSpan = Math.max(1, layout.visible.length);
        cell.setAttribute("role", "rowheader");
        const strong = document.createElement("strong");
        strong.textContent = label;
        const suffix = document.createElement("span");
        suffix.textContent = " (" + size + ")";
        cell.append(strong, suffix);
        heading.appendChild(cell);

        return heading;
    }

    // --- Row virtualisation -------------------------------------------------
    // A large page is rendered through a scroll window: only the rows covering
    // the viewport plus an overscan buffer are in the document, and two spacer
    // rows carry the height of everything above and below so the scrollbar
    // still describes the whole page. Row elements are moved, never rebuilt, so
    // a row that scrolls out and back returns with the same markup, the same
    // inline edits, and the same selection. Below virtualRowThreshold rows the
    // table renders in full and none of this machinery does any work.
    const virtualRowThreshold = 120;
    const virtualOverscanRows = 12;
    const virtualWindowLimit = 400;
    const minimumVirtualRowHeight = 16;
    const estimatedVirtualRowHeight = 34;

    let rowModel = orderedRows.map((row) => ({ node: row, group: false }));
    let rowOffsets = [];
    let rowWindow = { start: 0, end: 0 };
    let rowRenderFrame = 0;
    let rowRenderRemeasure = false;
    let virtualActive = false;
    const rowMetrics = { row: estimatedVirtualRowHeight, group: estimatedVirtualRowHeight, measured: false };
    const rowIndexBase = Math.max(0, Number(explorer.dataset.rowOffset) || 0);
    const serverRowCount = table ? Math.max(0, Number(table.getAttribute("aria-rowcount")) || 0) : 0;
    const selectionMirror = explorer.querySelector("[data-selection-mirror]");
    const topSpacerRow = spacerRow("top");
    const bottomSpacerRow = spacerRow("bottom");

    function spacerRow(position) {
        const row = document.createElement("tr");
        row.className = "results-virtual-spacer";
        row.dataset.virtualSpacer = position;
        row.setAttribute("role", "presentation");
        row.setAttribute("aria-hidden", "true");
        const cell = document.createElement("td");
        cell.setAttribute("role", "presentation");
        row.appendChild(cell);

        return row;
    }

    // rowIndexAtOffset returns the index of the row containing a pixel offset.
    // offsets is the ascending prefix sum of row heights, one entry longer than
    // the row model.
    function rowIndexAtOffset(offsets, value) {
        let low = 0;
        let high = Math.max(0, offsets.length - 2);
        while (low < high) {
            const middle = (low + high + 1) >> 1;
            if (offsets[middle] <= value) low = middle;
            else high = middle - 1;
        }

        return low;
    }

    // computeRowWindow returns the half-open [start, end) slice of the row
    // model that covers the viewport plus an overscan buffer. It is pure
    // arithmetic over the offsets, so the window it produces can be proved
    // bounded without a browser: end - start never exceeds maximumRows, however
    // many rows the page holds.
    function computeRowWindow(offsets, scrollTop, viewportHeight, overscan, maximumRows) {
        const total = Array.isArray(offsets) ? Math.max(0, offsets.length - 1) : 0;
        if (total <= 0) return { start: 0, end: 0 };
        const buffer = Math.max(0, Math.floor(Number(overscan) || 0));
        const limit = Math.max(1, Math.floor(Number(maximumRows) || 0));
        const height = Math.max(0, Number(viewportHeight) || 0);
        const top = Math.min(Math.max(0, Number(scrollTop) || 0), offsets[total]);
        const first = rowIndexAtOffset(offsets, top);
        const last = rowIndexAtOffset(offsets, top + height);
        const start = Math.max(0, first - buffer);
        const end = Math.min(total, Math.max(start + 1, last + 1 + buffer));

        return { start: start, end: Math.min(end, start + limit) };
    }

    function rowEntryHeight(entry) {
        return Math.max(minimumVirtualRowHeight, entry.group ? rowMetrics.group : rowMetrics.row);
    }

    function rebuildRowOffsets() {
        const offsets = new Array(rowModel.length + 1);
        offsets[0] = 0;
        for (let index = 0; index < rowModel.length; index += 1) {
            offsets[index + 1] = offsets[index] + rowEntryHeight(rowModel[index]);
        }
        rowOffsets = offsets;
    }

    // measureRowHeights reads one painted business row and, when grouping is
    // on, one painted group row. Every row of a kind is the same height (one
    // line per cell), so two measurements describe the whole page and no
    // per-row layout is ever forced.
    function measureRowHeights() {
        if (!tableBody) return false;
        let changed = false;
        const sample = tableBody.querySelector("[data-result-row]");
        if (sample) {
            const height = Math.round(sample.getBoundingClientRect().height);
            if (height >= minimumVirtualRowHeight && height !== rowMetrics.row) {
                rowMetrics.row = height;
                changed = true;
            }
        }
        const heading = tableBody.querySelector("[data-result-group]");
        if (heading) {
            const height = Math.round(heading.getBoundingClientRect().height);
            if (height >= minimumVirtualRowHeight && height !== rowMetrics.group) {
                rowMetrics.group = height;
                changed = true;
            }
        } else if (rowMetrics.group !== rowMetrics.row) {
            rowMetrics.group = rowMetrics.row;
            changed = true;
        }
        rowMetrics.measured = Boolean(sample);

        return changed;
    }

    function setSpacerHeight(row, height) {
        const cell = row.firstElementChild;
        row.hidden = height <= 0;
        if (!cell) return;
        cell.colSpan = Math.max(1, layout.visible.length);
        cell.style.height = Math.max(0, height) + "px";
    }

    // paintRowWindow reconciles the document against the requested window
    // instead of rewriting the whole body. Only the rows that actually leave or
    // enter are touched, so scrolling by one row costs one removal and one
    // insertion. That matters for more than speed: a row that is never detached
    // keeps the caret inside it, keeps an open inline edit, and gives the
    // browser nothing above the viewport to correct the scroll position for.
    function paintRowWindow(next) {
        const total = rowModel.length;
        setSpacerHeight(topSpacerRow, rowOffsets[next.start] || 0);
        setSpacerHeight(bottomSpacerRow, (rowOffsets[total] || 0) - (rowOffsets[next.end] || 0));

        const wanted = [topSpacerRow];
        for (let index = next.start; index < next.end; index += 1) wanted.push(rowModel[index].node);
        wanted.push(bottomSpacerRow);

        const keep = new Set(wanted);
        for (let child = tableBody.firstElementChild; child;) {
            const following = child.nextElementSibling;
            if (!keep.has(child)) tableBody.removeChild(child);
            child = following;
        }
        // What survived is already in the wanted order, so walking the two in
        // step inserts exactly the rows that are missing and moves nothing.
        let anchor = tableBody.firstElementChild;
        for (let index = 0; index < wanted.length; index += 1) {
            const node = wanted[index];
            if (node === anchor) {
                anchor = anchor.nextElementSibling;
                continue;
            }
            tableBody.insertBefore(node, anchor);
        }

        rowWindow = next;
        ensureFocusableCell();
        syncSelectionMirror();
    }

    // renderEveryRow is the safe degradation: a short page, and any page while
    // it is being printed, holds every row exactly as the server sent it.
    function renderEveryRow() {
        if (!tableBody) return;
        virtualActive = false;
        if (tableWrap) delete tableWrap.dataset.virtualRows;
        topSpacerRow.remove();
        bottomSpacerRow.remove();
        rowWindow = { start: 0, end: rowModel.length };
        tableBody.replaceChildren.apply(tableBody, rowModel.map((entry) => entry.node));
        syncSelectionMirror();
    }

    // renderRows is the only writer of table rows. It reads the scroll geometry
    // once, decides the window, and then writes; a scroll never measures a row.
    // A forced render (a layout, density, or grouping change) remeasures once
    // afterwards because the new rows may be a different height.
    function renderRows(force) {
        if (!tableBody) return;
        if (!tableWrap || rowModel.length < virtualRowThreshold) {
            if (virtualActive || force) renderEveryRow();
            return;
        }
        if (!virtualActive) {
            virtualActive = true;
            tableWrap.dataset.virtualRows = "true";
        }
        if (force || !rowMetrics.measured) measureRowHeights();
        if (force || rowOffsets.length !== rowModel.length + 1) rebuildRowOffsets();
        const next = computeRowWindow(rowOffsets, tableWrap.scrollTop, tableWrap.clientHeight,
            virtualOverscanRows, virtualWindowLimit);
        if (!force && next.start === rowWindow.start && next.end === rowWindow.end) return;
        paintRowWindow(next);
        if (!force) return;
        if (measureRowHeights()) {
            rebuildRowOffsets();
            paintRowWindow(computeRowWindow(rowOffsets, tableWrap.scrollTop, tableWrap.clientHeight,
                virtualOverscanRows, virtualWindowLimit));
        }
    }

    function scheduleRowRender(remeasure) {
        if (remeasure) rowRenderRemeasure = true;
        if (rowRenderFrame) return;
        rowRenderFrame = window.requestAnimationFrame(() => {
            rowRenderFrame = 0;
            const force = rowRenderRemeasure;
            rowRenderRemeasure = false;
            renderRows(force);
        });
    }

    // applyRowIndexes keeps the grid's row model true while it is windowed:
    // every row carries its real position in the result set and aria-rowcount
    // covers the whole set, so assistive technology never reports only the rows
    // that happen to be painted. The header row is row 1, so a data row at
    // model index i sits at rowIndexBase + i + 2.
    function applyRowIndexes() {
        if (!table) return;
        const headerRow = table.tHead && table.tHead.rows[0];
        if (headerRow) headerRow.setAttribute("aria-rowindex", "1");
        rowModel.forEach((entry, index) => {
            entry.node.setAttribute("aria-rowindex", String(rowIndexBase + index + 2));
        });
        table.setAttribute("aria-rowcount",
            String(Math.max(serverRowCount, rowIndexBase + rowModel.length) + 1));
    }

    // syncSelectionMirror keeps the bulk form honest while rows are windowed: a
    // checkbox that is not in the document is not submitted, so every selected
    // row outside the window contributes a hidden id in its place. It walks the
    // cached checkbox list, not the row model, so a scroll frame costs one pass
    // over the selection rather than one over the page, and it reads
    // isConnected — a plain tree flag — rather than measuring anything.
    function syncSelectionMirror() {
        if (!selectionMirror) return;
        const mirrored = [];
        if (virtualActive) {
            for (let index = 0; index < selectionCheckboxes.length; index += 1) {
                const checkbox = selectionCheckboxes[index];
                if (!checkbox.checked || checkbox.isConnected || !checkbox.value) continue;
                const field = document.createElement("input");
                field.type = "hidden";
                field.name = "result_ids";
                field.value = checkbox.value;
                mirrored.push(field);
            }
        }
        if (!mirrored.length && !selectionMirror.childElementCount) return;
        selectionMirror.replaceChildren.apply(selectionMirror, mirrored);
    }

    // revealRow brings a row the window has scrolled out of the document back
    // into it, so focus or a highlight has something to land on. It is a coarse
    // jump from the offsets and nothing more: a row that is already painted is
    // left exactly where it is, because scrollCellIntoView then positions it
    // from its measured rectangle rather than from an estimate.
    function revealRow(row) {
        if (!virtualActive || !tableWrap || !row || row.isConnected) return;
        const index = rowModel.findIndex((entry) => entry.node === row);
        if (index < 0) return;
        const headerHeight = table && table.tHead ? table.tHead.getBoundingClientRect().height : 0;
        tableWrap.scrollTop = Math.max(0, (rowOffsets[index] || 0) - headerHeight);
        renderRows(false);
    }

    // scrollCellIntoView is the grid's own scroll-into-view. The browser's does
    // not know that the sticky header covers the top of the scroll viewport, so
    // it will happily park a row underneath it; and because it runs after our
    // window is painted, its correction and the window can disagree by a row
    // and then chase each other. This measures the painted cell and moves the
    // scroll by exactly the shortfall, which cannot drift.
    function scrollCellIntoView(cell) {
        if (!tableWrap || !cell || !cell.isConnected) return;
        const headerHeight = table && table.tHead ? table.tHead.getBoundingClientRect().height : 0;
        const wrapBox = tableWrap.getBoundingClientRect();
        const cellBox = cell.getBoundingClientRect();
        const top = cellBox.top - wrapBox.top;
        const bottom = cellBox.bottom - wrapBox.top;
        if (top < headerHeight) tableWrap.scrollTop -= Math.ceil(headerHeight - top);
        else if (bottom > tableWrap.clientHeight) {
            tableWrap.scrollTop += Math.ceil(bottom - tableWrap.clientHeight);
        }
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
        // Density is written before the rows are rendered: the renderer
        // measures a painted row, so it has to measure it at the density the
        // operator just chose.
        if (table) table.dataset.density = layout.density;
        if (tableWrap) tableWrap.dataset.density = layout.density;
        reorderColumns();
        applyColumnVisibilityAndWidths();
        groupRows();
        // The density control is a segmented radio group, so the stored value
        // selects an input rather than writing to a single control's value.
        explorer.querySelectorAll("[data-layout-density]").forEach((control) => {
            if (control.type === "radio") control.checked = control.value === layout.density;
            else control.value = layout.density;
        });
        explorer.querySelectorAll("[data-layout-group]").forEach((control) => { control.value = layout.group; });
        setViewMode(layout.mode);
        syncProfileSelect();
        renderColumnControls();
        window.requestAnimationFrame(applyFrozenColumns);
        // Column widths and frozen offsets settle after the frame above, and a
        // row can be a different height once they do, so the window is
        // remeasured on the next frame rather than guessed at now.
        scheduleRowRender(true);
        if (persist) persistCurrentLayout();
        syncSavedViewLayout();
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

    // Selection state is recomputed from the cached checkbox list snapshotted
    // at the top of this script and only the rows that actually changed are
    // touched. Rebuilding the table on every click would thrash layout on a
    // large page for no benefit.
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
        syncSelectionMirror();
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

    // The grid keeps exactly one tab stop, tracked in a variable rather than
    // swept out of the document on every move: sweeping would mean touching
    // every cell on the page, which is the cost windowing exists to avoid.
    // The tab stop always sits on a painted cell, so Tab can always reach the
    // grid however far the operator has scrolled.
    let focusedCell = null;

    function setFocusedCell(cell) {
        if (focusedCell === cell) return;
        if (focusedCell) focusedCell.tabIndex = -1;
        focusedCell = cell || null;
        if (focusedCell) focusedCell.tabIndex = 0;
    }

    // The search starts at the window rather than at the top of the page, so
    // recovering the tab stop after a scroll costs one row, not one page.
    function ensureFocusableCell() {
        if (!table) return;
        if (focusedCell && !focusedCell.hidden && focusedCell.isConnected) return;
        const from = virtualActive ? rowWindow.start : 0;
        for (let index = from; index < rowModel.length; index += 1) {
            const entry = rowModel[index];
            if (entry.group || !entry.node.isConnected) continue;
            const cell = visibleCells(entry.node)[0];
            if (cell) {
                setFocusedCell(cell);
                return;
            }
        }
        setFocusedCell(null);
    }

    function focusCell(cell) {
        if (!cell || cell.hidden) return;
        revealRow(cell.closest("[data-result-row]"));
        setFocusedCell(cell);
        if (!virtualActive) {
            cell.focus();

            return;
        }
        // While the rows are windowed the grid does its own scrolling, so the
        // browser is asked to move focus and nothing else.
        cell.focus({ preventScroll: true });
        scrollCellIntoView(cell);
    }

    function handleTableKeydown(event) {
        if (!table) return;
        const cell = event.target.closest && event.target.closest("td[data-column]");
        const row = cell && cell.closest("[data-result-row]");
        if (!cell || !row) return;
        if (event.target.closest("[data-inline-edit]")) {
            if (event.key === "Escape") { event.preventDefault(); cancelInlineEdit(); }
            else if (event.key === "Enter") { event.preventDefault(); saveInlineEdit(); }
            return;
        }
        if (event.key === "F2" && cell.dataset.editField) {
            event.preventDefault();
            beginInlineEdit(cell);
            return;
        }
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

    // Inline editing routes every correction through the audited manual-edit
    // endpoint: a reason is mandatory, the server keeps the previous value as
    // superseded provenance plus a change row, and the toolbar keeps the last
    // edit undoable by posting the previous value back through the same route.
    const editableFieldLimits = { name: 200, phone: 40, website: 2048, category: 100 };
    const minimumEditReasonLength = 3;
    const maximumEditReasonLength = 300;
    const inlineEditTemplate = document.getElementById("inline-edit-template");
    const inlineEditUndo = explorer.querySelector("[data-inline-edit-undo]");
    let activeInlineEdit = null;
    let lastInlineEdit = null;

    function editableCell(candidate) {
        return candidate && candidate.dataset && candidate.dataset.editField ? candidate : null;
    }

    function validateInlineEdit(field, value, reason) {
        if (reason.length < minimumEditReasonLength || reason.length > maximumEditReasonLength) {
            return "Give a reason between " + minimumEditReasonLength + " and " + maximumEditReasonLength + " characters.";
        }
        const limit = editableFieldLimits[field];
        if (!limit) return "This field cannot be corrected here.";
        if (value.length > limit) return "This value must be at most " + limit + " characters.";
        if (field === "name" && !value) return "A business name is required.";
        if (field === "website" && value) {
            let parsed = null;
            try { parsed = new URL(value); } catch (_) { parsed = null; }
            if (!parsed || (parsed.protocol !== "http:" && parsed.protocol !== "https:")) {
                return "Enter an absolute http or https URL, or clear the field.";
            }
        }
        return "";
    }

    function hostOf(value) {
        try { return new URL(value).hostname.replace(/^www\./, ""); } catch (_) { return ""; }
    }

    // renderEditedCell writes the accepted value back into the cell that was
    // edited, keeping the rendered structure (link, stacked subtitle) intact so
    // an edited row still matches every other row on the page.
    function renderEditedCell(cell, field, value) {
        const row = cell.closest("[data-result-row]");
        cell.dataset.editValue = value;
        if (field === "name") {
            const link = cell.querySelector("a");
            if (link) { link.textContent = value; link.title = value; }
            cell.dataset.copyValue = value;
        } else if (field === "category") {
            const target = cell.querySelector(".truncate");
            if (target) { target.textContent = value || "—"; target.title = value; }
            cell.dataset.copyValue = value;
            if (row) row.dataset.groupCategory = value;
        } else if (field === "website") {
            const host = hostOf(value);
            const stack = cell.querySelector(".cell-stack");
            let target = stack ? stack.querySelector(".text-muted") : null;
            if (stack && !target) {
                target = document.createElement("span");
                target.className = "truncate text-muted";
                stack.appendChild(target);
            }
            if (target) target.textContent = host;
            cell.dataset.copyValue = host;
            if (row) { row.dataset.website = value; row.dataset.domain = host; }
        } else if (field === "phone") {
            const link = cell.querySelector("a");
            if (link && value) { link.textContent = value; link.setAttribute("href", "tel:" + value); }
            else {
                const text = document.createElement("span");
                text.textContent = value || "—";
                cell.replaceChildren(text);
            }
            cell.dataset.copyValue = value;
            if (row) row.dataset.phone = value;
        }
        if (layout.group === "category") groupRows();
    }

    function closeInlineEdit(restore) {
        if (!activeInlineEdit) return null;
        const state = activeInlineEdit;
        activeInlineEdit = null;
        state.cell.classList.remove("is-editing");
        if (restore) state.cell.replaceChildren.apply(state.cell, state.saved);
        return state;
    }

    function cancelInlineEdit() {
        const state = closeInlineEdit(true);
        if (state) focusCell(state.cell);
    }

    function beginInlineEdit(candidate) {
        const cell = editableCell(candidate);
        if (!cell || !inlineEditTemplate) return;
        if (activeInlineEdit && activeInlineEdit.cell === cell) return;
        cancelInlineEdit();
        const editor = inlineEditTemplate.content.firstElementChild.cloneNode(true);
        const valueInput = editor.querySelector("[data-inline-edit-value]");
        const valueLabel = editor.querySelector("[data-inline-edit-value-label]");
        const description = cell.dataset.editLabel || "New value";
        if (valueLabel) valueLabel.textContent = description;
        if (valueInput) {
            valueInput.value = cell.dataset.editValue || "";
            valueInput.setAttribute("aria-label", description);
            valueInput.maxLength = editableFieldLimits[cell.dataset.editField] || 200;
        }
        activeInlineEdit = { cell: cell, editor: editor, saved: Array.from(cell.childNodes) };
        cell.replaceChildren(editor);
        cell.classList.add("is-editing");
        if (valueInput) { valueInput.focus(); valueInput.select(); }
    }

    function inlineEditError(message) {
        if (!activeInlineEdit) return;
        const banner = activeInlineEdit.editor.querySelector("[data-inline-edit-error]");
        if (!banner) return;
        banner.textContent = message;
        banner.hidden = !message;
    }

    async function postFieldEdit(businessId, field, value, reason) {
        const csrf = explorer.querySelector('[name="csrf_token"]');
        const response = await fetch("/api/v1/results/" + encodeURIComponent(businessId) + "/fields", {
            method: "POST",
            credentials: "same-origin",
            headers: {
                "Accept": "application/json",
                "Content-Type": "application/json",
                "X-CSRF-Token": csrf ? csrf.value : ""
            },
            body: JSON.stringify({ field: field, value: value, reason: reason })
        });
        const payload = await response.json().catch(() => ({}));
        if (!response.ok) {
            throw new Error((payload.error && payload.error.message) || payload.message || "Could not save this correction.");
        }
        return payload.data || {};
    }

    async function saveInlineEdit() {
        if (!activeInlineEdit) return;
        const cell = activeInlineEdit.cell;
        const editor = activeInlineEdit.editor;
        const row = cell.closest("[data-result-row]");
        const businessId = row ? row.dataset.businessId : "";
        const field = cell.dataset.editField;
        const valueInput = editor.querySelector("[data-inline-edit-value]");
        const reasonInput = editor.querySelector("[data-inline-edit-reason]");
        const value = String(valueInput ? valueInput.value : "").trim();
        const reason = String(reasonInput ? reasonInput.value : "").trim();
        const problem = validateInlineEdit(field, value, reason);
        if (problem) {
            inlineEditError(problem);
            if (problem.indexOf("reason") !== -1 && reasonInput) reasonInput.focus();
            else if (valueInput) valueInput.focus();
            return;
        }
        inlineEditError("");
        const controls = Array.from(editor.querySelectorAll("input, button"));
        controls.forEach((control) => { control.disabled = true; });
        try {
            const saved = await postFieldEdit(businessId, field, value, reason);
            const previous = typeof saved.previous_value === "string" ? saved.previous_value : (cell.dataset.editValue || "");
            closeInlineEdit(true);
            renderEditedCell(cell, field, value);
            lastInlineEdit = { businessId: businessId, field: field, previous: previous, reason: reason };
            if (inlineEditUndo) { inlineEditUndo.hidden = false; inlineEditUndo.disabled = false; }
            focusCell(cell);
            announce("Saved the " + field + " correction. The previous value is kept in provenance.");
        } catch (error) {
            controls.forEach((control) => { control.disabled = false; });
            inlineEditError(error.message || "Could not save this correction.");
            announce(error.message || "Could not save this correction.", "error");
        }
    }

    async function undoInlineEdit(trigger) {
        if (!lastInlineEdit) return;
        const edit = lastInlineEdit;
        trigger.disabled = true;
        try {
            await postFieldEdit(edit.businessId, edit.field, edit.previous, "Undo of: " + edit.reason);
            const row = resultRows().find((candidate) => candidate.dataset.businessId === edit.businessId);
            const cell = row ? row.querySelector('[data-edit-field="' + edit.field + '"]') : null;
            if (cell) renderEditedCell(cell, edit.field, edit.previous);
            lastInlineEdit = null;
            if (inlineEditUndo) inlineEditUndo.hidden = true;
            announce("Restored the previous " + edit.field + " value. Both corrections stay in the record history.");
        } catch (error) {
            announce(error.message || "Could not undo this correction.", "error");
        } finally {
            trigger.disabled = false;
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
        const ids = selectionCheckboxes.filter((item) => item.checked).map((item) => item.value).filter(Boolean);
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
        if (event.target.matches("[data-column-profile]")) {
            if (event.target.value) applyColumnProfile(event.target.value);
            else syncProfileSelect();
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
        } else if (action === "save-inline-edit") {
            event.preventDefault();
            saveInlineEdit();
        } else if (action === "cancel-inline-edit") {
            event.preventDefault();
            cancelInlineEdit();
        } else if (action === "undo-inline-edit") {
            event.preventDefault();
            undoInlineEdit(trigger);
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
        table.addEventListener("dblclick", (event) => {
            const cell = event.target.closest && event.target.closest("td[data-edit-field]");
            if (!cell || event.target.closest("a, button, input, textarea, select")) return;
            event.preventDefault();
            beginInlineEdit(cell);
        });
        table.addEventListener("focusin", (event) => {
            const cell = event.target.closest && event.target.closest("td[data-column]");
            if (cell) setFocusedCell(cell);
        });
    }

    const filterForm = explorer.querySelector("[data-results-query-form]");
    // The filter controls live in a drawer and reach the toolbar's search form
    // through the form attribute, so they are read from form.elements rather
    // than by descendant lookup.
    if (filterForm) {
        const nestedInput = () => filterForm.elements.namedItem("filter_json");
        filterForm.addEventListener("submit", (event) => {
            const input = nestedInput();
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
        const nested = nestedInput();
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

    // Scrolling only ever schedules a frame; the frame does one geometry read
    // and one write. Scroll anchoring is turned off while the rows are
    // windowed because swapping the window under the browser would otherwise
    // make it correct the scroll position we just honoured.
    if (tableWrap) {
        tableWrap.addEventListener("scroll", () => scheduleRowRender(false), { passive: true });
        if (typeof window.ResizeObserver === "function") {
            new window.ResizeObserver(() => scheduleRowRender(true)).observe(tableWrap);
        } else {
            window.addEventListener("resize", () => scheduleRowRender(true));
        }
    }
    // Printing needs the whole page, not the window.
    window.addEventListener("beforeprint", renderEveryRow);
    window.addEventListener("afterprint", () => scheduleRowRender(true));

    explorer.querySelectorAll(".filter-row").forEach(updateFilterRow);
    restoreStoredProfile();
    populateLayoutSelect();
    populateProfileSelect();
    setupSortableHeaders();
    setupResizeHandles();
    applyLayout(layout, false);
    updateSelection();
})();
