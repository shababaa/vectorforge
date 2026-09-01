"use strict";

const PRICE_SCALE = 1_000_000;
const RATIO_SCALE = 1_000_000;
const state = { overview: null, controller: null };

const byId = (id) => document.getElementById(id);
const price = (value) => value == null ? "—" : new Intl.NumberFormat("en-US", { style: "currency", currency: "USD", minimumFractionDigits: 2, maximumFractionDigits: 4 }).format(value / PRICE_SCALE);
const percentPPM = (value, digits = 2) => value == null ? "—" : `${(value / RATIO_SCALE * 100).toFixed(digits)}%`;
const percent = (value, digits = 2) => value == null ? "—" : `${(value * 100).toFixed(digits)}%`;
const date = (seconds) => seconds ? new Date(seconds * 1000).toLocaleDateString("en-US", { timeZone: "UTC", year: "numeric", month: "short", day: "2-digit" }) : "—";
const number = (value, digits = 4) => Number.isFinite(value) ? value.toFixed(digits) : "—";

function setText(id, value) {
  byId(id).textContent = value;
}

function setSignal(id, label, direction) {
  const element = byId(id);
  element.textContent = label;
  element.classList.remove("positive", "negative", "neutral");
  element.classList.add(direction > 0 ? "positive" : direction < 0 ? "negative" : "neutral");
}

async function loadOverview() {
  if (state.controller) state.controller.abort();
  state.controller = new AbortController();
  byId("refresh").disabled = true;
  byId("error-banner").hidden = true;
  try {
    const response = await fetch("/api/v1/overview?symbol=SYNTH&interval=1d&limit=160", { signal: state.controller.signal, headers: { "Accept": "application/json" } });
    const body = await response.json();
    if (!response.ok) throw new Error(body.error?.message || `HTTP ${response.status}`);
    state.overview = body.data;
    render(body.data);
    setText("health-text", "Service ready");
    byId("health-dot").className = "status-dot ready";
  } catch (error) {
    if (error.name === "AbortError") return;
    byId("error-banner").textContent = `Dashboard request failed: ${error.message}`;
    byId("error-banner").hidden = false;
    setText("health-text", "Service unavailable");
    byId("health-dot").className = "status-dot error";
  } finally {
    byId("refresh").disabled = false;
  }
}

function render(data) {
  const candles = data.candles || [];
  const latest = candles.at(-1);
  const first = candles[0];
  setText("data-badge", `${data.symbol} · ${data.interval.toUpperCase()}`);
  setText("latest-close", price(latest?.adjusted_close));
  if (latest && first) {
    const change = latest.adjusted_close / first.adjusted_close - 1;
    setText("close-change", `${change >= 0 ? "+" : ""}${percent(change)} across ${candles.length} displayed bars`);
  }
  setText("return-one", percentPPM(data.feature?.return_1_ppm));
  setText("volatility", percentPPM(data.feature?.volatility_20_ppm));
  setText("updated-at", latest ? `Snapshot through ${date(latest.timestamp)} UTC · ${candles.length} displayed candles` : "No candles stored for this symbol.");
  setText("chart-start", date(first?.timestamp));
  setText("chart-end", date(latest?.timestamp));
  setText("db-detail", `Schema v5 · page version ${data.storage.version}`);
  setText("storage-detail", `${data.storage.page_count.toLocaleString()} pages · ${(data.storage.file_size_bytes / 1_048_576).toFixed(2)} MiB · ${data.storage.free_pages} reusable pages`);

  renderForecast(data.forecasts?.at(-1));
  renderModel(data.model);
  renderAnalysis(data.analysis);
  drawChart(candles, data.forecasts?.at(-1));
}

function renderForecast(forecast) {
  if (!forecast) {
    setText("coverage", "No forecast");
    return;
  }
  const delta = forecast.predicted_close - forecast.baseline_close;
  setText("coverage", percentPPM(forecast.confidence_ppm, 1));
  setText("forecast-baseline", price(forecast.baseline_close));
  setText("forecast-price", price(forecast.predicted_close));
  setText("forecast-low", price(forecast.lower_bound));
  setText("forecast-high", price(forecast.upper_bound));
  setText("forecast-target", `${date(forecast.target_timestamp)} UTC`);
  setSignal("forecast-direction", delta > 0 ? "Above baseline" : delta < 0 ? "Below baseline" : "Flat", delta);
}

function renderModel(model) {
  if (!model) return;
  const metrics = model.holdout.metrics;
  setText("model-name", `${model.selected_spec.name} selected by tuning`);
  setText("holdout-mae", price(Math.round(metrics.mae * PRICE_SCALE)));
  setText("mae-improvement", `${model.holdout.mae_improvement_vs_persistence >= 0 ? "+" : ""}${percent(model.holdout.mae_improvement_vs_persistence)}`);
  setText("direction-accuracy", percent(metrics.direction_accuracy));
  setText("observations", metrics.observations.toLocaleString());
  setSignal("baseline-result", model.holdout.beats_persistence_mae ? "Beat persistence" : "Did not beat baseline", model.holdout.beats_persistence_mae ? 1 : -1);
  setText("selection-note", `Chosen using tuning MAE ${number(model.tuning_mae, 6)} before the ${metrics.observations}-origin holdout was read. Dataset SHA-256: ${model.dataset_hash.slice(0, 16)}…`);
  byId("model-link").href = `/api/v1/model-cards/${encodeURIComponent(model.experiment_id)}`;
}

function renderAnalysis(analysis) {
  if (!analysis) return;
  setText("analysis-status", `${analysis.status} · ${analysis.provider}`);
  setText("analysis-thesis", analysis.thesis);
  const direction = analysis.outlook.startsWith("positive") ? 1 : analysis.outlook.startsWith("negative") ? -1 : 0;
  setSignal("analysis-outlook", analysis.outlook.replaceAll("_", " "), direction);
  const list = byId("evidence-list");
  list.replaceChildren();
  for (const claim of analysis.claims || []) {
    const item = document.createElement("li");
    const text = document.createElement("span");
    text.textContent = claim.text;
    const lineage = document.createElement("small");
    lineage.textContent = `Evidence: ${claim.id} → ${claim.record_id}`;
    lineage.className = "muted";
    lineage.style.display = "block";
    lineage.style.marginTop = ".35rem";
    item.append(text, lineage);
    list.append(item);
  }
  if (!list.children.length) {
    const item = document.createElement("li");
    item.textContent = "The analyst abstained or no cited claims were stored.";
    list.append(item);
  }
  byId("analysis-link").href = `/api/v1/analyses/${encodeURIComponent(analysis.analysis_id)}`;
}

function drawChart(candles, forecast) {
  const canvas = byId("price-chart");
  const bounds = canvas.getBoundingClientRect();
  const ratio = Math.min(window.devicePixelRatio || 1, 2);
  canvas.width = Math.max(1, Math.floor(bounds.width * ratio));
  canvas.height = Math.max(1, Math.floor(bounds.height * ratio));
  const context = canvas.getContext("2d");
  context.scale(ratio, ratio);
  const width = bounds.width;
  const height = bounds.height;
  const padding = { left: 58, right: 18, top: 15, bottom: 25 };
  context.clearRect(0, 0, width, height);
  if (!candles.length) return;
  const values = candles.map((candle) => candle.adjusted_close / PRICE_SCALE);
  if (forecast) values.push(forecast.lower_bound / PRICE_SCALE, forecast.upper_bound / PRICE_SCALE);
  let low = Math.min(...values);
  let high = Math.max(...values);
  const margin = Math.max((high - low) * .12, high * .005);
  low -= margin;
  high += margin;
  const plotWidth = width - padding.left - padding.right;
  const plotHeight = height - padding.top - padding.bottom;
  const x = (index) => padding.left + index / Math.max(candles.length, 1) * plotWidth;
  const y = (value) => padding.top + (high - value) / (high - low) * plotHeight;

  context.font = "11px ui-sans-serif, system-ui";
  context.textAlign = "right";
  context.fillStyle = "#6f8795";
  context.strokeStyle = "#203547";
  context.lineWidth = 1;
  for (let step = 0; step <= 4; step += 1) {
    const value = low + (high - low) * step / 4;
    const vertical = y(value);
    context.beginPath(); context.moveTo(padding.left, vertical); context.lineTo(width - padding.right, vertical); context.stroke();
    context.fillText(value.toFixed(2), padding.left - 8, vertical + 4);
  }
  context.beginPath();
  values.slice(0, candles.length).forEach((value, index) => index ? context.lineTo(x(index), y(value)) : context.moveTo(x(index), y(value)));
  context.strokeStyle = "#53d6bd";
  context.lineWidth = 2;
  context.stroke();
  if (forecast) {
    const lastX = x(candles.length - 1);
    const futureX = x(candles.length);
    const predicted = forecast.predicted_close / PRICE_SCALE;
    context.save();
    context.setLineDash([5, 5]);
    context.strokeStyle = "#f2b55a";
    context.beginPath(); context.moveTo(lastX, y(values[candles.length - 1])); context.lineTo(futureX, y(predicted)); context.stroke();
    context.restore();
    context.strokeStyle = "rgba(242, 181, 90, .45)";
    context.lineWidth = 5;
    context.beginPath(); context.moveTo(futureX, y(forecast.lower_bound / PRICE_SCALE)); context.lineTo(futureX, y(forecast.upper_bound / PRICE_SCALE)); context.stroke();
    context.fillStyle = "#f2b55a";
    context.beginPath(); context.arc(futureX, y(predicted), 4, 0, Math.PI * 2); context.fill();
  }
}

byId("refresh").addEventListener("click", loadOverview);
window.addEventListener("resize", () => state.overview && drawChart(state.overview.candles || [], state.overview.forecasts?.at(-1)));
loadOverview();
