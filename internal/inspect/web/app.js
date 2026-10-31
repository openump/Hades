"use strict";
// Renders the read-only snapshot. Values come from the plant, so every value is
// written with textContent, never as HTML markup: telemetry cannot inject tags.

function el(tag, text, cls) {
  const node = document.createElement(tag);
  if (text !== undefined && text !== null) node.textContent = String(text);
  if (cls) node.className = cls;
  return node;
}

function table(headers, rows) {
  const wrap = el("div", null, "wrap");
  const t = el("table");
  const head = el("tr");
  headers.forEach(h => head.appendChild(el("th", h)));
  t.appendChild(head);
  rows.forEach(cells => {
    const tr = el("tr");
    cells.forEach(c => {
      const td = el("td");
      if (c instanceof Node) td.appendChild(c);
      else td.textContent = c === null || c === undefined ? "" : (typeof c === "object" ? JSON.stringify(c) : String(c));
      tr.appendChild(td);
    });
    t.appendChild(tr);
  });
  wrap.appendChild(t);
  return wrap;
}

function badge(quality) {
  const q = (quality || "STALE").toUpperCase();
  return el("span", q, "badge " + q.toLowerCase());
}

function replace(id, node) {
  const target = document.getElementById(id);
  target.replaceChildren(node);
}

function render(s) {
  const uns = el("div");
  (s.assets || []).forEach(a => {
    uns.appendChild(el("h3", a.topic + " (" + a.asset_id + ")"));
    if (a.status) {
      const st = el("p", null, "note");
      st.appendChild(el("span", "Producer: " + (a.status.producer_state || "?") + ", acquisition: " + (a.status.acquisition_state || "?") + (a.status.reason ? " (" + a.status.reason + ")" : "")));
      uns.appendChild(st);
    }
    const rows = Object.keys(a.signals || {}).sort().map(name => {
      const sig = a.signals[name];
      return [name, sig.value, sig.unit, badge(sig.quality), sig.observed_at];
    });
    uns.appendChild(rows.length ? table(["Measurement", "Value", "Unit", "Quality", "Observed"], rows) : el("p", "No telemetry received yet.", "note"));
  });
  replace("uns", uns);

  const receipts = (s.receipts || []).slice().reverse().map(r => {
    const gates = el("span");
    (r.gates || []).forEach(g => gates.appendChild(el("span", "G" + g.gate + ":" + g.result + " ", "gate-" + g.result)));
    return [r.command_id, r.asset, r.target_parameter, r.state, gates, r.recorded_at];
  });
  replace("receipts", receipts.length ? table(["Command", "Asset", "Parameter", "State", "Gates", "Recorded"], receipts) : el("p", "No receipts received yet.", "note"));

  const q = el("dl");
  Object.keys(s.quarantine || {}).sort().forEach(k => { q.appendChild(el("dt", k)); q.appendChild(el("dd", s.quarantine[k])); });
  if (!q.childElementCount) q.appendChild(el("dd", "Normalizer health not configured."));
  replace("quarantine", q);

  const certs = (s.certificates || []).map(c => [c.subject, c.path, c.not_after, el("span", c.days_left + " days", "badge " + (c.days_left < 0 ? "bad" : c.days_left < 30 ? "uncertain" : "good"))]);
  replace("certificates", certs.length ? table(["Certificate", "File", "Expires", "Remaining"], certs) : el("p", "No certificates configured.", "note"));
  const svc = Object.keys(s.services || {}).sort().map(name => [name, JSON.stringify(s.services[name])]);
  replace("services", svc.length ? table(["Service", "Status"], svc) : el("p", "No supervised services configured.", "note"));
}

function connected(ok) {
  const c = document.getElementById("connection");
  c.textContent = ok ? "live" : "reconnecting";
  c.className = "badge " + (ok ? "good" : "stale");
}

function start() {
  const source = new EventSource("stream");
  source.addEventListener("snapshot", e => { connected(true); render(JSON.parse(e.data)); });
  source.addEventListener("heartbeat", () => connected(true));
  source.onerror = () => connected(false);
}

fetch("api/snapshot").then(r => r.json()).then(render).catch(() => connected(false));
start();
