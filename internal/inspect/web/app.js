"use strict";
// Renders the read-only snapshot. Values come from the plant, so every value is
// written with textContent, never as HTML markup: telemetry cannot inject tags.

// clean replaces control characters (including ESC and newlines) and Unicode
// bidirectional formatting characters with U+FFFD, the same as hades top: a
// plant value can neither inject markup (textContent) nor reorder text to
// disguise what it says.
function clean(text) {
  return String(text).replace(/[\u0000-\u001f\u007f-\u009f\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069]/g, "\ufffd");
}

function el(tag, text, cls) {
  const node = document.createElement(tag);
  if (text !== undefined && text !== null) node.textContent = clean(text);
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
      else {
        const text = c === null || c === undefined ? "" : clean(typeof c === "object" ? JSON.stringify(c) : String(c));
        td.textContent = text;
        // Short tokens (names, timestamps) keep their width; medium ones (IDs,
        // URNs, states) wrap at natural breaks; only long free text may wrap
        // anywhere, so hostile text cannot squeeze columns.
        td.className = text.length > 40 ? "long" : text.length > 24 ? "" : "short";
      }
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

  const q = document.getElementById("quarantine");
  const items = [];
  Object.keys(s.quarantine || {}).sort().forEach(k => { items.push(el("dt", k), el("dd", s.quarantine[k])); });
  if (!items.length) items.push(el("dd", "Normalizer health not configured."));
  q.replaceChildren(...items);

  const certs = (s.certificates || []).map(c => [c.subject, c.path, c.not_after, el("span", c.days_left + " days", "badge " + (c.days_left < 0 ? "bad" : c.days_left < 30 ? "uncertain" : "good"))]);
  replace("certificates", certs.length ? table(["Certificate", "File", "Expires", "Remaining"], certs) : el("p", "No certificates configured.", "note"));
  const svc = Object.keys(s.services || {}).sort().map(path => service(path, s.services[path] || {}));
  replace("services", svc.length ? table(["Service", "Ready", "Digest", "PID", "Revision", "Started", "Last output / error"], svc) : el("p", "No supervised services configured.", "note"));
}

// service renders one supervised service as readable fields (the same as the
// supervisor pane of hades top). Times come from the snapshot, never the
// browser clock, so the view is reproducible.
function service(path, e) {
  const name = el("span");
  const short = path.split("/").pop().replace(/\.json$/, "");
  name.appendChild(el("strong", short, short.length > 40 ? "long" : "nowrap"));
  name.appendChild(el("div", path, "note"));
  const ready = el("span", e.ready ? "ready" : "not ready", "badge " + (e.ready ? "good" : "bad"));
  const digest = e.digest_verified === true ? el("span", "verified", "badge good")
    : e.digest_verified === false ? el("span", "MISMATCH", "badge bad")
    : el("span", "unknown", "badge uncertain"); // supervisor unreachable: not a mismatch
  const detail = e.error ? el("span", e.error, "error") : when(e.last_output_at);
  return [name, ready, digest, e.child_pid || "", e.revision || "", when(e.started_at), detail];
}

// when hides Go's zero time ("no output yet") instead of showing year 1.
function when(t) {
  return !t || String(t).startsWith("0001-01-01") ? "" : t;
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
