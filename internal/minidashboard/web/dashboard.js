(() => {
  "use strict";

  const schemaVersion = "loom.mini_dashboard.v1";
  const displayTimeZone = "Europe/Amsterdam";
  const rowKeys = {
    system: ["cpu", "temperature", "memory", "storage"],
    runtime: ["loomd", "database", "workers", "queue"],
    protection: ["local", "cloud", "coverage", "findings"],
  };

  const byID = (id) => document.getElementById(id);
  const setText = (id, value) => {
    byID(id).textContent = String(value ?? "Unavailable");
  };

  function bounded(value, fallback, maxLength) {
    const text = String(value ?? "").trim();
    return text && text.length <= maxLength ? text : fallback;
  }

  function renderRows(group, rows) {
    if (!Array.isArray(rows) || rows.length !== 4) {
      throw new Error(`${group} must contain exactly four rows`);
    }
    const expected = rowKeys[group];
    const container = byID(`${group}-rows`);
    container.replaceChildren();
    rows.forEach((row, index) => {
      const wrapper = document.createElement("div");
      wrapper.className = "metric-row";
      wrapper.dataset.key = expected[index];
      wrapper.dataset.severity = bounded(row.severity, "unknown", 10);
      wrapper.dataset.state = bounded(row.state, "unavailable", 12);

      const marker = document.createElement("span");
      marker.className = "metric-marker";
      marker.setAttribute("aria-hidden", "true");
      const label = document.createElement("span");
      label.className = "metric-label";
      label.textContent = bounded(row.label, "Unavailable", 16);
      const value = document.createElement("span");
      value.className = "metric-value";
      value.textContent = bounded(row.value, "Unavailable", 18);
      wrapper.append(marker, label, value);
      container.append(wrapper);
    });
  }

  function ageLabel(updatedAt, now) {
    const timestamp = Date.parse(updatedAt);
    if (!Number.isFinite(timestamp)) return "--";
    const seconds = Math.max(0, Math.floor((now - timestamp) / 1000));
    if (seconds < 60) return `${seconds}s`;
    if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
    if (seconds < 172800) return `${Math.floor(seconds / 3600)}h`;
    return `${Math.floor(seconds / 86400)}d`;
  }

  function renderClock(now = new Date()) {
    setText("clock", new Intl.DateTimeFormat("en-GB", {
      hour: "2-digit",
      minute: "2-digit",
      hour12: false,
      timeZone: displayTimeZone,
    }).format(now));
  }

  function renderFixture(snapshot, now = new Date()) {
    if (!snapshot || snapshot.schema_version !== schemaVersion) {
      throw new Error(`fixture must use ${schemaVersion}`);
    }
    const overall = bounded(snapshot.header?.overall, "unknown", 10);
    const freshnessState = bounded(snapshot.header?.freshness_state, "unknown", 12);
    const age = ageLabel(snapshot.header?.source_updated_at, now);
    const freshnessWords = {
      live: "LIVE",
      cached: "CACHED",
      stale: "STALE",
      offline: "OFFLINE",
      unavailable: "UNAVAILABLE",
      failed: "FAILED",
      disabled: "DISABLED",
      unknown: "UNKNOWN",
    };
    const freshnessWord = freshnessWords[freshnessState] ?? "UNKNOWN";

    byID("dashboard").dataset.overall = overall;
    setText("node-label", bounded(snapshot.header?.node_label, "MAIN", 20));
    setText("overall-state", overall.toUpperCase());
    byID("overall-state").dataset.severity = overall;
    setText("freshness", `${freshnessWord} ${age}`);
    byID("freshness").dataset.state = freshnessState;

    const attentionSeverity = bounded(snapshot.attention?.severity, "unknown", 10);
    const attentionBand = document.querySelector(".attention-band");
    attentionBand.dataset.severity = attentionSeverity;
    setText("attention-message", bounded(snapshot.attention?.message, "Status unavailable", 64));
    const additional = Number(snapshot.attention?.additional_count || 0);
    const attentionMore = byID("attention-more");
    attentionMore.hidden = additional < 1;
    attentionMore.textContent = additional > 0 ? `+${additional} more` : "";

    renderRows("system", snapshot.system);
    renderRows("runtime", snapshot.runtime);
    renderRows("protection", snapshot.protection);

    setText("network-state", bounded(snapshot.network?.state, "NETWORK", 20));
    setText("network-detail", bounded(snapshot.network?.detail, "Network unavailable", 90));
    document.querySelector(".network-band").dataset.severity = bounded(snapshot.network?.severity, "unknown", 10);
    setText("activity-state", bounded(snapshot.activity?.state, "IDLE", 16));
    setText("activity-detail", bounded(snapshot.activity?.detail, "No recent activity", 90));
    document.querySelector(".activity-band").dataset.severity = bounded(snapshot.activity?.severity, "unknown", 10);

    renderClock(now);
    document.documentElement.dataset.ready = "true";
    document.documentElement.dataset.connection = "online";
  }

  let lastSnapshot = null;

  async function pollStatus() {
    const controller = new AbortController();
    const timeout = window.setTimeout(() => controller.abort(), 1500);
    try {
      const response = await fetch("/api/status", { cache: "no-store", credentials: "same-origin", signal: controller.signal });
      if (!response.ok) throw new Error(`status ${response.status}`);
      const snapshot = await response.json();
      if (!snapshot || snapshot.schema_version !== schemaVersion) throw new Error("unsupported status schema");
      lastSnapshot = snapshot;
      const generatedAt = Date.parse(snapshot.generated_at);
      renderFixture(snapshot, Number.isFinite(generatedAt) ? new Date(generatedAt) : new Date());
    } catch (_error) {
      document.documentElement.dataset.connection = "offline";
      if (!lastSnapshot) document.documentElement.dataset.ready = "waiting";
    } finally {
      window.clearTimeout(timeout);
    }
  }

  function start() {
    renderClock();
    void pollStatus();
    window.setInterval(renderClock, 1000);
    window.setInterval(pollStatus, 2000);
  }

  window.renderMiniDashboardFixture = renderFixture;
  window.renderMiniDashboardClock = renderClock;
  window.pollMiniDashboardStatus = pollStatus;
  start();
})();
