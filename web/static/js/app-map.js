(function () {
    "use strict";

    const explorer = document.querySelector("[data-map-explorer]");
    if (!explorer) return;

    const canvas = explorer.querySelector("[data-map-canvas]");
    const unavailable = explorer.querySelector("[data-map-unavailable]");
    const status = explorer.querySelector("[data-map-status]");
    const areaSelect = explorer.querySelector("[data-map-area-select]");
    const areaName = explorer.querySelector("[data-map-area-name]");
    const latitudeInput = explorer.querySelector("[data-map-latitude]");
    const longitudeInput = explorer.querySelector("[data-map-longitude]");
    const radiusInput = explorer.querySelector("[data-map-radius]");
    const cellSizeInput = explorer.querySelector("[data-map-cell-size]");
    const queryInput = explorer.querySelector("[data-map-query]");
    const jobSelect = explorer.querySelector("[data-map-job]");
    const heatmapSelect = explorer.querySelector("[data-map-heatmap]");
    const liveRefreshInput = explorer.querySelector("[data-map-live-refresh]");
    const cellKeywordInput = explorer.querySelector("[data-map-cell-keyword]");
    const keywordGroupSelect = explorer.querySelector("[data-map-keyword-group]");
    const cellCount = explorer.querySelector("[data-map-cell-count]");
    const markerCount = explorer.querySelector("[data-map-marker-count]");
    const heatLegend = explorer.querySelector("[data-map-heat-legend]");
    const densityHeatButton = explorer.querySelector('[data-action="toggle-density-heat"]');
    const failedHeatButton = explorer.querySelector('[data-action="toggle-failed-heat"]');
    const emptyHeatButton = explorer.querySelector('[data-action="toggle-empty-heat"]');
    const duplicateHeatButton = explorer.querySelector('[data-action="toggle-duplicate-heat"]');
    const csrfToken = explorer.dataset.csrfToken || "";
    const areasEndpoint = explorer.dataset.areasEndpoint || "/api/v1/maps/areas";
    const gridEndpoint = explorer.dataset.gridEndpoint || "/api/v1/maps/grid/preview";
    const coverageEndpoint = explorer.dataset.coverageEndpoint || "/api/v1/maps/grid/coverage";
    const resultsEndpoint = explorer.dataset.resultsEndpoint || "/api/v1/maps/results";
    const resultsExportEndpoint = explorer.dataset.resultsExportEndpoint || "/api/v1/maps/results/export";
    const rescrapeEndpoint = explorer.dataset.rescrapeEndpoint || "/api/v1/maps/cells/rescrape";
    let areaID = explorer.dataset.areaId || "";
    let currentProperties = {};
    let lastPreview = null;
    let selectedCells = new Set();
    let removedCells = new Set();
    let map;
    let drawnLayers;
    let gridLayers;
    let resultLayers;
    let heatLayers;
    let liveRefreshTimer = null;
    let densityHeatOn = false;
    let lastResultPoints = [];
    let lastDensityMaximum = 0;
    const coverageEmphasis = { failed: false, empty: false, duplicates: false };

    // Heat ramps are duplicated as .heat-density-*, .heat-failed-*, .heat-duplicate-*,
    // .heat-empty-cell, and .heat-muted-cell classes in app.css so the legend
    // swatches always match.
    const densityHeatRamp = ["#bcd3f5", "#6ea0ea", "#3478e5", "#1d4ea8"];
    const failedHeatRamp = ["#f3c6cd", "#e78d9b", "#d44b5c", "#9c2130"];
    const duplicateHeatRamp = ["#e2d2f3", "#c3a2e4", "#9f66d3", "#6d3aa8"];
    const emptyHeatFill = "#e8b45a";
    const emptyHeatStroke = "#8a6a1c";
    const mutedHeatFill = "#d9dee7";
    const mutedHeatStroke = "#aab3c2";

    function showStatus(message, tone) {
        if (status) {
            status.textContent = message;
            status.dataset.tone = tone || "neutral";
        }
    }

    function showError(error) {
        const message = error && error.message ? error.message : "The map action could not be completed.";
        showStatus(message, "error");
        if (window.GMapsApp && typeof window.GMapsApp.toast === "function") {
            window.GMapsApp.toast(message, "error");
        }
    }

    async function requestJSON(url, options) {
        const requestOptions = Object.assign({ credentials: "same-origin" }, options || {});
        requestOptions.headers = Object.assign({ Accept: "application/json" }, requestOptions.headers || {});
        if (requestOptions.method && requestOptions.method !== "GET") {
            requestOptions.headers["X-CSRF-Token"] = csrfToken;
        }
        const response = await fetch(url, requestOptions);
        let payload;
        try {
            payload = await response.json();
        } catch (_) {
            payload = null;
        }
        if (!response.ok) {
            const message = payload && payload.error && payload.error.message
                ? payload.error.message
                : "Map request failed with status " + response.status + ".";
            throw new Error(message);
        }
        return payload || {};
    }

    function cloneProperties(properties) {
        try {
            return JSON.parse(JSON.stringify(properties || {}));
        } catch (_) {
            return {};
        }
    }

    function validNumber(input, minimum, maximum, label) {
        const value = Number(input && input.value);
        if (!Number.isFinite(value) || value < minimum || value > maximum) {
            throw new Error(label + " must be between " + minimum + " and " + maximum + ".");
        }
        return value;
    }

    function setAreaState(nextID) {
        areaID = nextID || "";
        explorer.dataset.areaId = areaID;
        if (areaSelect) areaSelect.value = areaID;
        explorer.querySelectorAll('[data-action="update-area"], [data-action="delete-area"], [data-action="export-area"]').forEach((button) => {
            button.disabled = !areaID;
        });
        const exportLink = document.querySelector("[data-map-export]");
        if (exportLink && areaID) exportLink.href = areasEndpoint + "/" + encodeURIComponent(areaID) + "/export";
    }

    function updateSelectionActions() {
        const removeButton = explorer.querySelector('[data-action="remove-cells"]');
        const restoreButton = explorer.querySelector('[data-action="restore-cells"]');
        if (removeButton) {
            removeButton.disabled = selectedCells.size === 0;
            removeButton.textContent = selectedCells.size > 0 ? "Remove selected (" + selectedCells.size + ")" : "Remove selected";
        }
        if (restoreButton) {
            restoreButton.disabled = removedCells.size === 0;
            restoreButton.textContent = removedCells.size > 0 ? "Restore removed (" + removedCells.size + ")" : "Restore removed";
        }
        const retryButton = explorer.querySelector('[data-action="retry-cells"]');
        const keywordButton = explorer.querySelector('[data-action="keyword-cells"]');
        const groupButton = explorer.querySelector('[data-action="group-cells"]');
        if (retryButton) retryButton.disabled = selectedCells.size === 0 || !(jobSelect && jobSelect.value);
        if (keywordButton) keywordButton.disabled = selectedCells.size === 0 || !(jobSelect && jobSelect.value);
        if (groupButton) groupButton.disabled = selectedCells.size === 0 || !(jobSelect && jobSelect.value) || !(keywordGroupSelect && keywordGroupSelect.value);
    }

    function clearDerivedLayers() {
        lastPreview = null;
        selectedCells = new Set();
        if (gridLayers) gridLayers.clearLayers();
        if (resultLayers) resultLayers.clearLayers();
        if (heatLayers) heatLayers.clearLayers();
        lastResultPoints = [];
        lastDensityMaximum = 0;
        if (cellCount) cellCount.textContent = "0";
        if (markerCount) markerCount.textContent = "0";
        updateSelectionActions();
        updateHeatLegend();
    }

    function layerShape(layer) {
        return layer && layer._gosomMapShape ? layer._gosomMapShape : "polygon";
    }

    function featureFromLayers() {
        const layers = drawnLayers.getLayers();
        if (!layers.length) throw new Error("Draw an area or load a saved area first.");

        const properties = cloneProperties(currentProperties);
        properties.excluded_cells = Array.from(removedCells).sort();
        if (!properties.excluded_cells.length) delete properties.excluded_cells;

        if (layers.length === 1 && layerShape(layers[0]) === "circle") {
            const centre = layers[0].getLatLng();
            delete properties.bbox;
            properties.shape = "circle";
            properties.radius_m = layers[0].getRadius();
            return {
                type: "Feature",
                properties: properties,
                geometry: { type: "Point", coordinates: [centre.lng, centre.lat] }
            };
        }

        if (layers.length === 1 && layerShape(layers[0]) === "bbox") {
            const bounds = layers[0].getBounds();
            const bbox = [bounds.getWest(), bounds.getSouth(), bounds.getEast(), bounds.getNorth()];
            delete properties.radius_m;
            properties.shape = "bbox";
            properties.bbox = bbox;
            return {
                type: "Feature",
                bbox: bbox,
                properties: properties,
                geometry: layers[0].toGeoJSON().geometry
            };
        }

        const polygons = [];
        layers.forEach((layer) => {
            const geometry = layer.toGeoJSON().geometry;
            if (!geometry) return;
            if (geometry.type === "Polygon") polygons.push(geometry.coordinates);
            if (geometry.type === "MultiPolygon") geometry.coordinates.forEach((polygon) => polygons.push(polygon));
        });
        if (!polygons.length) throw new Error("The active area does not contain a polygon.");
        delete properties.radius_m;
        delete properties.bbox;
        properties.shape = polygons.length === 1 ? "polygon" : "multipolygon";
        return {
            type: "Feature",
            properties: properties,
            geometry: polygons.length === 1
                ? { type: "Polygon", coordinates: polygons[0] }
                : { type: "MultiPolygon", coordinates: polygons }
        };
    }

    function updateCoordinateInputs(layer) {
        let centre;
        if (layerShape(layer) === "circle") {
            centre = layer.getLatLng();
            if (radiusInput) radiusInput.value = (layer.getRadius() / 1000).toFixed(3).replace(/0+$/, "").replace(/\.$/, "");
        } else if (layer.getBounds) {
            centre = layer.getBounds().getCenter();
        }
        if (!centre) return;
        if (latitudeInput) latitudeInput.value = centre.lat.toFixed(7).replace(/0+$/, "").replace(/\.$/, "");
        if (longitudeInput) longitudeInput.value = centre.lng.toFixed(7).replace(/0+$/, "").replace(/\.$/, "");
    }

    function fitArea() {
        const bounds = drawnLayers.getBounds();
        if (bounds && bounds.isValid()) map.fitBounds(bounds.pad(0.12), { maxZoom: 16 });
    }

    function replaceGeometry(feature, shouldFit) {
        if (!feature || feature.type !== "Feature") throw new Error("Expected one GeoJSON Feature.");
        drawnLayers.clearLayers();
        gridLayers.clearLayers();
        resultLayers.clearLayers();
        selectedCells = new Set();
        lastPreview = null;
        currentProperties = cloneProperties(feature.properties);
        removedCells = new Set(Array.isArray(currentProperties.excluded_cells) ? currentProperties.excluded_cells.filter((id) => typeof id === "string") : []);

        const shape = String(currentProperties.shape || "").toLowerCase();
        if (shape === "circle" && feature.geometry && feature.geometry.type === "Point") {
            const coordinates = feature.geometry.coordinates || [];
            const radius = Number(currentProperties.radius_m || currentProperties.radius_metres || currentProperties.radius_meters);
            if (coordinates.length < 2 || !Number.isFinite(radius) || radius <= 0) throw new Error("The saved circle is invalid.");
            const circle = window.L.circle([coordinates[1], coordinates[0]], { radius: radius, color: "#2d6cdf", fillOpacity: 0.12 });
            circle._gosomMapShape = "circle";
            drawnLayers.addLayer(circle);
            updateCoordinateInputs(circle);
        } else {
            const geoJSONLayer = window.L.geoJSON(feature, {
                style: { color: "#2d6cdf", weight: 2, fillOpacity: 0.10 },
                onEachFeature: function (_, layer) {
                    layer._gosomMapShape = shape === "bbox" ? "bbox" : "polygon";
                }
            });
            geoJSONLayer.eachLayer((layer) => {
                drawnLayers.addLayer(layer);
                updateCoordinateInputs(layer);
            });
        }
        if (!drawnLayers.getLayers().length) throw new Error("The GeoJSON feature has no drawable geometry.");
        if (shouldFit !== false) fitArea();
        updateSelectionActions();
    }

    function applyCoordinateCircle() {
        const latitude = validNumber(latitudeInput, -90, 90, "Latitude");
        const longitude = validNumber(longitudeInput, -180, 180, "Longitude");
        const radiusKM = validNumber(radiusInput, 0.001, 500, "Radius");
        currentProperties = {};
        removedCells = new Set();
        replaceGeometry({
            type: "Feature",
            properties: { shape: "circle", radius_m: radiusKM * 1000 },
            geometry: { type: "Point", coordinates: [longitude, latitude] }
        }, true);
        setAreaState("");
        showStatus("Coordinate circle applied. Preview the grid or save it as a reusable area.", "success");
    }

    const stateColours = {
        waiting: "#778195",
        running: "#3478e5",
        completed: "#2f9e70",
        partial: "#d18a12",
        failed: "#d44b5c",
        blocked: "#d44b5c",
        paused: "#8654c7"
    };

    function cellStyle(cell, selected) {
        const state = stateColours[cell.state] ? cell.state : "waiting";
        const overlay = heatmapSelect ? heatmapSelect.value : "coverage";
        let fillColor = stateColours[state];
        let fillOpacity = selected ? 0.43 : 0.25;
        if (overlay !== "coverage") {
            let value = 0;
            if (overlay === "density") value = Number(cell.result_count || 0);
            if (overlay === "failed") value = Number(cell.failed_tasks || 0) + Number(cell.blocked_tasks || 0);
            if (overlay === "empty") value = cell.empty ? 1 : 0;
            if (overlay === "duplicates") value = Number(cell.duplicate_count || 0);
            let maximum = 1;
            if (lastPreview && Array.isArray(lastPreview.cells)) {
                maximum = Math.max(1, ...lastPreview.cells.map((item) => {
                    if (overlay === "density") return Number(item.result_count || 0);
                    if (overlay === "failed") return Number(item.failed_tasks || 0) + Number(item.blocked_tasks || 0);
                    if (overlay === "empty") return item.empty ? 1 : 0;
                    return Number(item.duplicate_count || 0);
                }));
            }
            const intensity = Math.max(0, Math.min(1, value / maximum));
            const hues = { density: 212, failed: 351, empty: 42, duplicates: 276 };
            fillColor = value > 0 ? "hsl(" + hues[overlay] + " 72% " + (68 - intensity * 30) + "%)" : "#d9dee7";
            fillOpacity = selected ? 0.64 : (value > 0 ? 0.30 + intensity * 0.45 : 0.14);
        }
        const style = {
            color: selected ? "#101828" : stateColours[state],
            weight: selected ? 3 : 1.5,
            dashArray: selected ? "5 3" : null,
            fillColor: fillColor,
            fillOpacity: fillOpacity
        };

        return applyCoverageEmphasis(cell, style, selected);
    }

    function heatStep(value, maximum) {
        const intensity = value / Math.max(1, maximum);
        if (intensity <= 0.25) return 1;
        if (intensity <= 0.5) return 2;
        if (intensity <= 0.75) return 3;
        return 4;
    }

    function cellFailureCount(cell) {
        return Number(cell.failed_tasks || 0) + Number(cell.blocked_tasks || 0);
    }

    function maximumCellFailures() {
        if (!lastPreview || !Array.isArray(lastPreview.cells)) return 0;
        return lastPreview.cells.reduce((maximum, cell) => Math.max(maximum, cellFailureCount(cell)), 0);
    }

    function cellDuplicateCount(cell) {
        return Number(cell.duplicates || 0);
    }

    function maximumCellDuplicates() {
        if (!lastPreview || !Array.isArray(lastPreview.cells)) return 0;
        return lastPreview.cells.reduce((maximum, cell) => Math.max(maximum, cellDuplicateCount(cell)), 0);
    }

    function applyCoverageEmphasis(cell, style, selected) {
        if (!coverageEmphasis.failed && !coverageEmphasis.empty && !coverageEmphasis.duplicates) return style;
        const failures = cellFailureCount(cell);
        if (coverageEmphasis.failed && failures > 0) {
            const step = heatStep(failures, maximumCellFailures());
            style.fillColor = failedHeatRamp[step - 1];
            style.fillOpacity = selected ? 0.68 : 0.32 + step * 0.09;
            if (!selected) style.color = failedHeatRamp[3];
            return style;
        }
        if (coverageEmphasis.empty && cell.empty) {
            style.fillColor = emptyHeatFill;
            style.fillOpacity = selected ? 0.68 : 0.55;
            if (!selected) style.color = emptyHeatStroke;
            return style;
        }
        if (coverageEmphasis.duplicates && cellDuplicateCount(cell) > 0) {
            const step = heatStep(cellDuplicateCount(cell), maximumCellDuplicates());
            style.fillColor = duplicateHeatRamp[step - 1];
            style.fillOpacity = selected ? 0.68 : 0.32 + step * 0.09;
            if (!selected) style.color = duplicateHeatRamp[3];
            return style;
        }
        style.fillColor = mutedHeatFill;
        style.fillOpacity = selected ? 0.4 : 0.08;
        if (!selected) style.color = mutedHeatStroke;
        return style;
    }

    function restyleGridCells() {
        if (!gridLayers) return;
        gridLayers.eachLayer((layer) => {
            if (layer._gosomCell) layer.setStyle(cellStyle(layer._gosomCell, selectedCells.has(layer._gosomCell.id)));
        });
    }

    function coverageEvidenceAvailable() {
        return Boolean(lastPreview && Array.isArray(lastPreview.cells) && lastPreview.cells.some((cell) =>
            Number(cell.task_count || 0) > 0 || Number(cell.result_count || 0) > 0));
    }

    function setCoverageEmphasis(kind, button) {
        const next = !coverageEmphasis[kind];
        if (next && !coverageEvidenceAvailable()) {
            showStatus("No durable coverage evidence is loaded yet. Choose a source job and refresh coverage, then enable heat shading.", "warning");
            return;
        }
        coverageEmphasis[kind] = next;
        if (button) button.setAttribute("aria-pressed", next ? "true" : "false");
        restyleGridCells();
        updateHeatLegend();
        if (!next) {
            const offMessages = {
                failed: "Failed-cell heat shading off; coverage colours restored.",
                empty: "Empty-cell emphasis off; coverage colours restored.",
                duplicates: "Duplicate-heavy shading off; coverage colours restored."
            };
            showStatus(offMessages[kind], "success");
            return;
        }
        const cells = lastPreview && Array.isArray(lastPreview.cells) ? lastPreview.cells : [];
        if (kind === "duplicates") {
            const affected = cells.filter((cell) => cellDuplicateCount(cell) > 0).length;
            showStatus(affected > 0
                ? "Duplicate-heavy shading on: " + affected + " cells recorded skipped or replaced duplicate rows (darker purple means more duplicates)."
                : "Duplicate-heavy shading on: no cell has recorded duplicate rows yet, so everything is shown muted grey.", affected > 0 ? "success" : "warning");
            return;
        }
        if (kind === "failed") {
            const affected = cells.filter((cell) => cellFailureCount(cell) > 0).length;
            showStatus(affected > 0
                ? "Failed-cell heat shading on: " + affected + " cells carry failed or blocked tasks (darker red means more failures)."
                : "Failed-cell heat shading on: no cells carry failed or blocked tasks, so everything is shown muted grey.", affected > 0 ? "success" : "warning");
            return;
        }
        const affected = cells.filter((cell) => cell.empty).length;
        showStatus(affected > 0
            ? "Empty-cell emphasis on: " + affected + " completed cells produced zero results (amber)."
            : "Empty-cell emphasis on: every completed cell produced at least one result, so everything is shown muted grey.", affected > 0 ? "success" : "warning");
    }

    function heatLegendRow(swatchClass, label) {
        const row = document.createElement("span");
        const swatch = document.createElement("i");
        swatch.className = "legend-swatch heat-swatch " + swatchClass;
        row.appendChild(swatch);
        row.appendChild(document.createTextNode(label));
        return row;
    }

    function heatRampRows(prefix, unitSingular, unitPlural, maximum) {
        const rows = [];
        let lower = 1;
        for (let step = 1; step <= 4; step++) {
            const upper = Math.max(1, Math.ceil(Math.max(1, maximum) * step * 0.25));
            if (lower > upper) continue;
            const label = (lower === upper ? String(upper) : lower + "–" + upper) + " " + (upper === 1 ? unitSingular : unitPlural);
            rows.push(heatLegendRow(prefix + step, label));
            lower = upper + 1;
        }
        return rows;
    }

    function updateHeatLegend() {
        if (!heatLegend) return;
        heatLegend.replaceChildren();
        const mode = explorer.dataset.mode || "planning";
        const addSection = function (title, rows) {
            if (!rows.length) return;
            const heading = document.createElement("span");
            heading.className = "heat-legend-title";
            heading.textContent = title;
            heatLegend.appendChild(heading);
            rows.forEach((row) => heatLegend.appendChild(row));
        };
        if (mode === "results" && densityHeatOn && lastResultPoints.length) {
            addSection("Result density per bucket:", heatRampRows("heat-density-", "result", "results", lastDensityMaximum));
        }
        if (mode !== "results" && coverageEmphasis.failed) {
            const rows = heatRampRows("heat-failed-", "failed or blocked task", "failed or blocked tasks", maximumCellFailures());
            rows.push(heatLegendRow("heat-muted-cell", "no failures (muted)"));
            addSection("Failed-cell shading:", rows);
        }
        if (mode !== "results" && coverageEmphasis.empty) {
            addSection("Empty-cell emphasis:", [
                heatLegendRow("heat-empty-cell", "completed with zero results (amber)"),
                heatLegendRow("heat-muted-cell", "other cells (muted)")
            ]);
        }
        if (mode !== "results" && coverageEmphasis.duplicates) {
            const rows = heatRampRows("heat-duplicate-", "duplicate row (purple)", "duplicate rows (purple)", maximumCellDuplicates());
            rows.push(heatLegendRow("heat-muted-cell", "no recorded duplicates (muted)"));
            addSection("Duplicate-heavy shading:", rows);
        }
        heatLegend.hidden = !heatLegend.childNodes.length;
    }

    function densityBucketSizeDegrees() {
        const centre = map.getCenter();
        const centrePoint = map.latLngToContainerPoint(centre);
        const shifted = map.containerPointToLatLng(window.L.point(centrePoint.x + 56, centrePoint.y + 56));
        return {
            latitude: Math.max(Math.abs(shifted.lat - centre.lat), 0.0001),
            longitude: Math.max(Math.abs(shifted.lng - centre.lng), 0.0001)
        };
    }

    function renderDensityHeat() {
        if (!heatLayers || !map) return;
        heatLayers.clearLayers();
        lastDensityMaximum = 0;
        if (!densityHeatOn || !lastResultPoints.length) {
            updateHeatLegend();
            return;
        }
        const size = densityBucketSizeDegrees();
        const buckets = new Map();
        lastResultPoints.forEach((point) => {
            const row = Math.floor(point.latitude / size.latitude);
            const column = Math.floor(point.longitude / size.longitude);
            const key = row + ":" + column;
            const bucket = buckets.get(key) || { row: row, column: column, count: 0 };
            bucket.count++;
            buckets.set(key, bucket);
        });
        let maximum = 1;
        buckets.forEach((bucket) => { maximum = Math.max(maximum, bucket.count); });
        lastDensityMaximum = maximum;
        buckets.forEach((bucket) => {
            const step = heatStep(bucket.count, maximum);
            const colour = densityHeatRamp[step - 1];
            const rectangle = window.L.rectangle([
                [bucket.row * size.latitude, bucket.column * size.longitude],
                [(bucket.row + 1) * size.latitude, (bucket.column + 1) * size.longitude]
            ], { color: colour, weight: 1, fillColor: colour, fillOpacity: 0.28 + step * 0.11 });
            rectangle.bindTooltip(bucket.count + (bucket.count === 1 ? " result" : " results") + " in this bucket");
            heatLayers.addLayer(rectangle);
        });
        updateHeatLegend();
    }

    function syncResultLayerVisibility() {
        if (!map || !heatLayers) return;
        if (densityHeatOn) {
            if (map.hasLayer(resultLayers)) map.removeLayer(resultLayers);
            if (!map.hasLayer(heatLayers)) map.addLayer(heatLayers);
            renderDensityHeat();
        } else {
            if (map.hasLayer(heatLayers)) map.removeLayer(heatLayers);
            if (!map.hasLayer(resultLayers)) map.addLayer(resultLayers);
        }
    }

    function setDensityHeat(next) {
        densityHeatOn = next;
        if (densityHeatButton) densityHeatButton.setAttribute("aria-pressed", next ? "true" : "false");
        if (explorer.dataset.mode === "results") syncResultLayerVisibility();
        updateHeatLegend();
        if (!next) {
            showStatus("Density heatmap off. Result markers are clustered again.", "success");
            return;
        }
        if (!lastResultPoints.length) {
            showStatus("Density heatmap is on, but no mapped results are loaded yet. Load results to fill the intensity buckets.", "warning");
            return;
        }
        showStatus("Density heatmap on: " + lastResultPoints.length + " mapped results aggregated (darker blue means more results per bucket).", "success");
    }

    function cellTooltip(cell) {
        const parts = ["Cell " + cell.number, cell.state || "waiting"];
        if (Number(cell.task_count || 0)) parts.push((cell.completed_tasks || 0) + "/" + cell.task_count + " tasks");
        if (Number(cell.result_count || 0)) parts.push(cell.result_count + " results");
        if (Number(cell.duplicate_count || 0)) parts.push(cell.duplicate_count + " duplicates");
        if (Number(cell.duplicates || 0)) parts.push(cell.duplicates + " duplicate rows skipped or replaced");
        if (cell.empty) parts.push("empty");
        if (Number(cell.failed_tasks || 0) || Number(cell.blocked_tasks || 0)) {
            parts.push((Number(cell.failed_tasks || 0) + Number(cell.blocked_tasks || 0)) + " failed/blocked");
        }
        return parts.join(" · ");
    }

    function renderPreview(preview) {
        lastPreview = preview;
        selectedCells = new Set();
        gridLayers.clearLayers();
        const cells = Array.isArray(preview.cells) ? preview.cells : [];
        let visibleCount = 0;
        cells.forEach((cell) => {
            if (!cell || removedCells.has(cell.id)) return;
            const bounds = cell.bounds || {};
            const rectangle = window.L.rectangle([
                [bounds.min_latitude, bounds.min_longitude],
                [bounds.max_latitude, bounds.max_longitude]
            ], cellStyle(cell, false));
            rectangle._gosomCell = cell;
            rectangle.bindTooltip(cellTooltip(cell));
            rectangle.on("click", function () {
                if (selectedCells.has(cell.id)) selectedCells.delete(cell.id);
                else selectedCells.add(cell.id);
                rectangle.setStyle(cellStyle(cell, selectedCells.has(cell.id)));
                updateSelectionActions();
            });
            gridLayers.addLayer(rectangle);
            visibleCount++;
        });
        if (cellCount) cellCount.textContent = String(visibleCount);
        updateSelectionActions();
        updateHeatLegend();
    }

    function renderPreviewPreservingSelection(preview) {
        const preserved = new Set(selectedCells);
        renderPreview(preview);
        const visible = new Set(Array.isArray(preview.cells) ? preview.cells.map((cell) => cell.id) : []);
        selectedCells = new Set(Array.from(preserved).filter((id) => visible.has(id) && !removedCells.has(id)));
        gridLayers.eachLayer((layer) => {
            if (layer._gosomCell) layer.setStyle(cellStyle(layer._gosomCell, selectedCells.has(layer._gosomCell.id)));
        });
        updateSelectionActions();
    }

    async function previewGrid() {
        const feature = featureFromLayers();
        const cellSize = validNumber(cellSizeInput, 0.05, 100, "Grid cell size");
        explorer.setAttribute("aria-busy", "true");
        showStatus("Calculating the deterministic grid…");
        try {
            const payload = await requestJSON(gridEndpoint, {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ geojson: feature, cell_size_km: cellSize })
            });
            renderPreview(payload.data || {});
            setMode(explorer.dataset.mode || "planning", false);
            const total = payload.data && payload.data.cells ? payload.data.cells.length : 0;
            showStatus(total + " deterministic cells calculated; " + removedCells.size + " excluded.", "success");
        } finally {
            explorer.removeAttribute("aria-busy");
        }
    }

    function mapGeometryRequest() {
        return {
            geojson: featureFromLayers(),
            cell_size_km: validNumber(cellSizeInput, 0.05, 100, "Grid cell size")
        };
    }

    async function loadCoverage(silent) {
        if (!jobSelect || !jobSelect.value) {
            if (!silent) throw new Error("Choose a source job before loading live coverage.");
            return;
        }
        const request = mapGeometryRequest();
        request.job_id = jobSelect.value;
        if (!silent) showStatus("Loading durable cell coverage…");
        const payload = await requestJSON(coverageEndpoint, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(request)
        });
        renderPreviewPreservingSelection(payload.data || {});
        setMode("live", false);
        const summary = payload.data && payload.data.summary ? payload.data.summary : {};
        showStatus("Coverage: " + (summary.completed_cells || 0) + " completed, " + (summary.running_cells || 0) + " running, " + (summary.failed_cells || 0) + " failed, " + (summary.empty_cells || 0) + " empty.", "success");
    }

    function selectedCellRequest(action) {
        if (!jobSelect || !jobSelect.value) throw new Error("Choose a source job first.");
        if (!selectedCells.size) throw new Error("Select one or more grid cells first.");
        const request = mapGeometryRequest();
        request.job_id = jobSelect.value;
        request.cell_ids = Array.from(selectedCells).sort();
        request.action = action;
        if (action === "keyword") request.keyword = cellKeywordInput ? cellKeywordInput.value.trim() : "";
        if (action === "template") request.template_id = keywordGroupSelect ? keywordGroupSelect.value : "";
        return request;
    }

    async function queueSelectedCells(action) {
        const request = selectedCellRequest(action);
        if (action === "retry" && !window.confirm("Queue a new compatible job for the selected failed or empty cells?")) return;
        const payload = await requestJSON(rescrapeEndpoint, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(request)
        });
        const data = payload.data || {};
        showStatus("Queued " + (data.name || "selected-cell scrape") + ".", "success");
        if (data.url && window.confirm("Selected-cell job queued. Open its monitor now?")) window.location.assign(data.url);
    }

    async function exportResults(format) {
        const response = await fetch(resultsExportEndpoint + "?format=" + encodeURIComponent(format), {
            method: "POST",
            credentials: "same-origin",
            headers: { Accept: format === "geojson" ? "application/geo+json" : "text/csv", "Content-Type": "application/json", "X-CSRF-Token": csrfToken },
            body: JSON.stringify({ geojson: featureFromLayers(), search: resultSearchFromURL() })
        });
        if (!response.ok) {
            let message = "Area export failed with status " + response.status + ".";
            try {
                const payload = await response.json();
                message = payload.error && payload.error.message ? payload.error.message : message;
            } catch (_) {
                // A proxy or aborted response may not provide a JSON error body.
            }
            throw new Error(message);
        }
        const blob = await response.blob();
        const disposition = response.headers.get("Content-Disposition") || "";
        const match = disposition.match(/filename="?([^";]+)"?/i);
        const objectURL = URL.createObjectURL(blob);
        const link = document.createElement("a");
        link.href = objectURL;
        link.download = match ? match[1] : "map-businesses." + (format === "geojson" ? "geojson" : "csv");
        document.body.appendChild(link);
        link.click();
        link.remove();
        URL.revokeObjectURL(objectURL);
        showStatus("Exported businesses inside the drawn area.", "success");
    }

    function removeSelectedCells() {
        selectedCells.forEach((id) => removedCells.add(id));
        selectedCells = new Set();
        if (lastPreview) renderPreview(lastPreview);
        showStatus(removedCells.size + " cells are excluded. Update or save the area to persist this selection.", "success");
    }

    function restoreRemovedCells() {
        removedCells = new Set();
        if (lastPreview) renderPreview(lastPreview);
        showStatus("All deterministic grid cells restored.", "success");
    }

    function combineGroups(left, right) {
        if (!left) return right;
        if (!right) return left;
        return { logic: "and", groups: [left, right] };
    }

    function resultSearchFromURL() {
        const parameters = new URLSearchParams(window.location.search);
        const fields = parameters.getAll("filter_field");
        const operators = parameters.getAll("filter_operator");
        const values = parameters.getAll("filter_value");
        if (fields.length !== operators.length || fields.length !== values.length) {
            throw new Error("The current Results URL contains incomplete filter rows.");
        }
        let filters = fields.map((field, index) => ({
            field: field,
            operator: operators[index],
            value: values[index]
        })).filter((filter) => filter.field && filter.operator);
        let filterGroup = null;
        const filterJSON = parameters.get("filter_json");
        if (filterJSON) {
            try { filterGroup = JSON.parse(filterJSON); }
            catch (_) { throw new Error("The current Results URL contains invalid nested filter JSON."); }
        }
        if ((parameters.get("filter_logic") || "").toLowerCase() === "or" && filters.length) {
            filterGroup = combineGroups(filterGroup, { logic: "or", filters: filters });
            filters = [];
        }
        return {
            query: queryInput ? queryInput.value.trim() : "",
            job_id: jobSelect ? jobSelect.value : "",
            sort: parameters.get("sort") || "",
            filters: filters,
            filter_group: filterGroup,
            include_duplicates: parameters.get("include_duplicates") === "true",
            limit: 250,
            offset: 0
        };
    }

    function appendPopupLine(container, label, value) {
        if (value === undefined || value === null || value === "") return;
        const line = document.createElement("p");
        const heading = document.createElement("strong");
        heading.textContent = label + ": ";
        line.appendChild(heading);
        line.appendChild(document.createTextNode(String(value)));
        container.appendChild(line);
    }

    function safeExternalLink(url) {
        try {
            const parsed = new URL(url, window.location.origin);
            return parsed.protocol === "http:" || parsed.protocol === "https:" ? parsed.href : "";
        } catch (_) {
            return "";
        }
    }

    function resultPopup(result) {
        const popup = document.createElement("div");
        popup.className = "map-result-popup";
        const title = document.createElement("strong");
        title.className = "map-result-title";
        title.textContent = result.name || "Unnamed business";
        popup.appendChild(title);
        appendPopupLine(popup, "Category", result.primary_category);
        appendPopupLine(popup, "Address", result.address);
        const rating = result.rating === null || result.rating === undefined ? "" : result.rating + " (" + (result.review_count || 0) + " reviews)";
        appendPopupLine(popup, "Rating", rating);
        appendPopupLine(popup, "Phone", result.phone);
        appendPopupLine(popup, "Email", result.primary_email);
        appendPopupLine(popup, "Website", result.website_status || (result.website ? "available" : "not recorded"));
        if (Number.isFinite(result.quality_score)) appendPopupLine(popup, "Quality", Math.round(result.quality_score) + "%");

        const links = document.createElement("div");
        links.className = "map-popup-links";
        if (result.id) {
            const details = document.createElement("a");
            details.href = "/app/results/" + encodeURIComponent(result.id);
            details.textContent = "Details";
            links.appendChild(details);
        }
        [[result.maps_url, "Google Maps"], [result.website, "Website"]].forEach((entry) => {
            const href = safeExternalLink(entry[0]);
            if (!href) return;
            const link = document.createElement("a");
            link.href = href;
            link.textContent = entry[1];
            link.target = "_blank";
            link.rel = "noopener noreferrer";
            links.appendChild(link);
        });
        if (links.childNodes.length) popup.appendChild(links);
        return popup;
    }

    async function loadResults() {
        const feature = featureFromLayers();
        explorer.setAttribute("aria-busy", "true");
        showStatus("Applying spatial and advanced result filters…");
        try {
            const payload = await requestJSON(resultsEndpoint, {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ geojson: feature, search: resultSearchFromURL() })
            });
            resultLayers.clearLayers();
            const results = Array.isArray(payload.data) ? payload.data : [];
            let plotted = 0;
            const points = [];
            results.forEach((result) => {
                const latitude = Number(result.latitude);
                const longitude = Number(result.longitude);
                if (!Number.isFinite(latitude) || !Number.isFinite(longitude)) return;
                const marker = window.L.marker([latitude, longitude], { title: result.name || "Business result" });
                marker.bindPopup(resultPopup(result), { maxWidth: 340 });
                resultLayers.addLayer(marker);
                points.push({ latitude: latitude, longitude: longitude });
                plotted++;
            });
            lastResultPoints = points;
            if (markerCount) markerCount.textContent = String(plotted);
            setMode("results", false);
            const total = payload.meta && Number.isFinite(Number(payload.meta.total)) ? Number(payload.meta.total) : results.length;
            showStatus("Showing " + plotted + " mapped businesses from " + total + " spatial matches (up to 250 per view).", "success");
        } finally {
            explorer.removeAttribute("aria-busy");
        }
    }

    async function refreshAreas(selectedID) {
        if (!areaSelect) return;
        const payload = await requestJSON(areasEndpoint + "?limit=100");
        const areas = Array.isArray(payload.data) ? payload.data.slice() : [];
        areas.sort((left, right) => String(left.name || "").localeCompare(String(right.name || "")));
        areaSelect.replaceChildren();
        const empty = document.createElement("option");
        empty.value = "";
        empty.textContent = "Unsaved area";
        areaSelect.appendChild(empty);
        areas.forEach((area) => {
            const option = document.createElement("option");
            option.value = area.id;
            option.textContent = area.name;
            areaSelect.appendChild(option);
        });
        areaSelect.value = selectedID || areaID || "";
    }

    function applyLoadedArea(area) {
        if (!area || !area.id || !area.geojson) throw new Error("The saved area response is incomplete.");
        replaceGeometry(area.geojson, true);
        setAreaState(area.id);
        if (areaName) areaName.value = area.name || "";
        showStatus("Loaded saved area “" + (area.name || area.id) + "”.", "success");
    }

    async function loadArea() {
        const selectedID = areaSelect ? areaSelect.value : areaID;
        if (!selectedID) {
            setAreaState("");
            showStatus("The current drawing is now an unsaved area.");
            return;
        }
        const payload = await requestJSON(areasEndpoint + "/" + encodeURIComponent(selectedID));
        applyLoadedArea(payload.data);
        await previewGrid();
    }

    async function saveArea(update) {
        const name = areaName ? areaName.value.trim() : "";
        if (!name) throw new Error("Enter an area name before saving.");
        if (update && !areaID) throw new Error("Load a saved area before updating it.");
        const feature = featureFromLayers();
        feature.properties = feature.properties || {};
        feature.properties.name = name;
        const endpoint = update ? areasEndpoint + "/" + encodeURIComponent(areaID) : areasEndpoint;
        const payload = await requestJSON(endpoint, {
            method: update ? "PUT" : "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ name: name, geojson: feature })
        });
        applyLoadedArea(payload.data);
        await refreshAreas(payload.data.id);
        showStatus(update ? "Saved-area geometry and exclusions updated." : "New reusable area saved locally.", "success");
    }

    async function deleteArea() {
        if (!areaID) throw new Error("Load a saved area before deleting it.");
        if (!window.confirm("Delete this saved area? Jobs and business results are kept.")) return;
        await requestJSON(areasEndpoint + "/" + encodeURIComponent(areaID), { method: "DELETE" });
        setAreaState("");
        if (areaName) areaName.value = "";
        await refreshAreas("");
        showStatus("Saved area deleted. The current drawing remains available until you leave the page.", "success");
    }

    function exportArea() {
        if (!areaID) throw new Error("Save or load an area before exporting it.");
        const link = document.createElement("a");
        link.href = areasEndpoint + "/" + encodeURIComponent(areaID) + "/export";
        link.download = "map-area-" + areaID + ".geojson";
        document.body.appendChild(link);
        link.click();
        link.remove();
    }

    async function importArea(file) {
        if (!file) return;
        if (file.size > 1024 * 1024) throw new Error("GeoJSON imports must be 1 MiB or smaller.");
        const raw = await file.text();
        try { JSON.parse(raw); }
        catch (_) { throw new Error("The selected file is not valid JSON."); }
        const fallbackName = file.name.replace(/\.(geojson|json)$/i, "");
        const payload = await requestJSON(areasEndpoint + "/import?name=" + encodeURIComponent(fallbackName), {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: raw
        });
        const areas = Array.isArray(payload.data) ? payload.data : [];
        if (!areas.length) throw new Error("The import did not contain a supported area.");
        applyLoadedArea(areas[0]);
        await refreshAreas(areas[0].id);
        await previewGrid();
        showStatus("Imported " + areas.length + " saved area" + (areas.length === 1 ? "." : "s."), "success");
    }

    function setMode(mode, load) {
        const normalized = mode === "results" || mode === "live" ? mode : "planning";
        explorer.dataset.mode = normalized;
        explorer.querySelectorAll('input[name="mode"]').forEach((input) => { input.checked = input.value === normalized; });
        if (normalized === "results") {
            if (map.hasLayer(gridLayers)) map.removeLayer(gridLayers);
            syncResultLayerVisibility();
            if (load !== false) loadResults().catch(showError);
        } else {
            if (map.hasLayer(resultLayers)) map.removeLayer(resultLayers);
            if (map.hasLayer(heatLayers)) map.removeLayer(heatLayers);
            if (!map.hasLayer(gridLayers)) map.addLayer(gridLayers);
            if (normalized === "live" && load !== false) loadCoverage(false).catch(showError);
        }
        updateLiveRefresh();
        updateHeatLegend();
    }

    function updateLiveRefresh() {
        if (liveRefreshTimer) {
            window.clearInterval(liveRefreshTimer);
            liveRefreshTimer = null;
        }
        if (explorer.dataset.mode !== "live" || !liveRefreshInput || !liveRefreshInput.checked || !jobSelect || !jobSelect.value) return;
        liveRefreshTimer = window.setInterval(function () {
            if (!document.hidden) loadCoverage(true).catch(showError);
        }, 5000);
    }

    function startDrawing(shape) {
        const options = shape === "polygon" ? { allowIntersection: false, showArea: true, shapeOptions: { color: "#2d6cdf" } } : { shapeOptions: { color: "#2d6cdf" } };
        let drawer;
        if (shape === "polygon") drawer = new window.L.Draw.Polygon(map, options);
        if (shape === "bbox") drawer = new window.L.Draw.Rectangle(map, options);
        if (shape === "circle") drawer = new window.L.Draw.Circle(map, options);
        if (drawer) drawer.enable();
    }

    function initializeMap() {
        if (!canvas || !window.L || !window.L.Draw) {
            if (unavailable) unavailable.hidden = false;
            showStatus("The locally bundled interactive map could not start.", "error");
            return false;
        }
        map = window.L.map(canvas, { preferCanvas: true, zoomControl: true }).setView([37.7749, -122.4194], 11);
        const tiles = window.L.tileLayer(explorer.dataset.tileTemplate, {
            minZoom: 0,
            maxZoom: 19,
            attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap contributors</a>'
        });
        let reportedTileError = false;
        tiles.on("tileerror", function () {
            if (!reportedTileError) {
                reportedTileError = true;
                showStatus("A base tile is not cached and could not be downloaded; drawing and local data remain available.", "warning");
            }
        });
        tiles.addTo(map);

        drawnLayers = window.L.featureGroup().addTo(map);
        gridLayers = window.L.featureGroup().addTo(map);
        resultLayers = typeof window.L.markerClusterGroup === "function"
            ? window.L.markerClusterGroup({ chunkedLoading: true, showCoverageOnHover: false })
            : window.L.layerGroup();
        heatLayers = window.L.layerGroup();

        map.on("zoomend", function () {
            if (densityHeatOn && (explorer.dataset.mode || "") === "results") renderDensityHeat();
        });

        map.addControl(new window.L.Control.Draw({
            position: "topleft",
            edit: { featureGroup: drawnLayers, remove: true },
            draw: {
                marker: false,
                circlemarker: false,
                polyline: false,
                polygon: { allowIntersection: false, showArea: true, shapeOptions: { color: "#2d6cdf" } },
                rectangle: { shapeOptions: { color: "#2d6cdf" } },
                circle: { shapeOptions: { color: "#2d6cdf" } }
            }
        }));

        map.on(window.L.Draw.Event.CREATED, function (event) {
            drawnLayers.clearLayers();
            currentProperties = {};
            removedCells = new Set();
            event.layer._gosomMapShape = event.layerType === "rectangle" ? "bbox" : event.layerType;
            drawnLayers.addLayer(event.layer);
            updateCoordinateInputs(event.layer);
            setAreaState("");
            clearDerivedLayers();
            fitArea();
            showStatus("New " + event.layerType + " area drawn. Preview or save it.", "success");
        });
        map.on(window.L.Draw.Event.EDITED, function (event) {
            event.layers.eachLayer(updateCoordinateInputs);
            removedCells = new Set();
            setAreaState("");
            clearDerivedLayers();
            showStatus("Area geometry edited. Preview the grid again.", "success");
        });
        map.on(window.L.Draw.Event.DELETED, function () {
            removedCells = new Set();
            setAreaState("");
            clearDerivedLayers();
            showStatus("Area geometry removed. Draw or load another area.");
        });

        const initial = document.getElementById("map-initial-geojson");
        if (initial && initial.value.trim()) replaceGeometry(JSON.parse(initial.value), true);
        else applyCoordinateCircle();
        setMode(explorer.dataset.mode || "planning", false);
        return true;
    }

    explorer.addEventListener("click", function (event) {
        const trigger = event.target.closest("[data-action]");
        if (!trigger) return;
        const action = trigger.dataset.action;
        const actions = {
            "load-area": loadArea,
            "save-area": function () { return saveArea(false); },
            "update-area": function () { return saveArea(true); },
            "delete-area": deleteArea,
            "export-area": exportArea,
            "apply-circle": applyCoordinateCircle,
            "preview-grid": previewGrid,
            "fit-area": fitArea,
            "remove-cells": removeSelectedCells,
            "restore-cells": restoreRemovedCells,
            "load-coverage": function () { return loadCoverage(false); },
            "retry-cells": function () { return queueSelectedCells("retry"); },
            "keyword-cells": function () { return queueSelectedCells("keyword"); },
            "group-cells": function () { return queueSelectedCells("template"); },
            "load-results": loadResults,
            "toggle-density-heat": function () { setDensityHeat(!densityHeatOn); },
            "toggle-failed-heat": function () { setCoverageEmphasis("failed", failedHeatButton); },
            "toggle-empty-heat": function () { setCoverageEmphasis("empty", emptyHeatButton); },
            "toggle-duplicate-heat": function () { setCoverageEmphasis("duplicates", duplicateHeatButton); },
            "export-results-csv": function () { return exportResults("csv"); },
            "export-results-geojson": function () { return exportResults("geojson"); },
            "draw-polygon": function () { startDrawing("polygon"); },
            "draw-bbox": function () { startDrawing("bbox"); },
            "draw-circle": function () { startDrawing("circle"); }
        };
        if (!actions[action]) return;
        event.preventDefault();
        try {
            const outcome = actions[action]();
            if (outcome && typeof outcome.catch === "function") outcome.catch(showError);
        } catch (error) {
            showError(error);
        }
    });

    explorer.addEventListener("change", function (event) {
        if (event.target.name === "mode") setMode(event.target.value, true);
        if (event.target === jobSelect) {
            updateSelectionActions();
            updateLiveRefresh();
            if (explorer.dataset.mode === "live" && jobSelect.value) loadCoverage(false).catch(showError);
        }
        if (event.target === heatmapSelect && lastPreview) renderPreviewPreservingSelection(lastPreview);
        if (event.target === liveRefreshInput) updateLiveRefresh();
        if (event.target === keywordGroupSelect) updateSelectionActions();
        if (event.target.matches("[data-map-import]")) {
            importArea(event.target.files && event.target.files[0]).catch(showError).finally(() => { event.target.value = ""; });
        }
    });

    if (queryInput) queryInput.addEventListener("keydown", function (event) {
        if (event.key === "Enter") {
            event.preventDefault();
            loadResults().catch(showError);
        }
    });

    try {
        if (initializeMap()) {
            previewGrid().then(function () {
                if ((explorer.dataset.mode || "") === "results") return loadResults();
                if ((explorer.dataset.mode || "") === "live" && jobSelect && jobSelect.value) return loadCoverage(false);
                return null;
            }).catch(showError);
        }
    } catch (error) {
        if (unavailable) unavailable.hidden = false;
        showError(error);
    }
})();
