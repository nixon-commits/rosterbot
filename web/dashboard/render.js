// render.js — shared DOM helpers: HTML-escaping and a generic JSON->DOM
// renderer used for the run-output viewer (arrays of objects -> table, plain
// objects -> key/value rows, everything else -> a simple list/text node).
export function escapeHtml(value) {
  const div = document.createElement("div");
  div.textContent = String(value);
  return div.innerHTML;
}

export function renderAuto(data) {
  if (data === null || data === undefined) return textNode("(empty)");
  if (Array.isArray(data)) {
    if (data.length === 0) return textNode("(empty list)");
    if (typeof data[0] === "object" && data[0] !== null) return renderTable(data);
    return renderList(data);
  }
  if (typeof data === "object") return renderKeyValue(data);
  return textNode(String(data));
}

function textNode(text) {
  const p = document.createElement("p");
  p.className = "muted";
  p.textContent = text;
  return p;
}

function renderList(items) {
  const ul = document.createElement("ul");
  for (const item of items) {
    const li = document.createElement("li");
    li.textContent = String(item);
    ul.appendChild(li);
  }
  return ul;
}

function renderKeyValue(obj) {
  const table = document.createElement("table");
  table.className = "kv-table";
  for (const [key, value] of Object.entries(obj)) {
    const row = document.createElement("tr");
    const th = document.createElement("th");
    th.textContent = key;
    const td = document.createElement("td");
    if (value !== null && typeof value === "object") {
      td.appendChild(renderAuto(value));
    } else {
      td.textContent = value === null || value === undefined ? "" : String(value);
    }
    row.append(th, td);
    table.appendChild(row);
  }
  return table;
}

function renderTable(rows) {
  // Union of keys across all rows, in first-seen order, so a row missing an
  // optional field doesn't shift columns.
  const cols = [];
  const seen = new Set();
  for (const row of rows) {
    for (const key of Object.keys(row)) {
      if (!seen.has(key)) {
        seen.add(key);
        cols.push(key);
      }
    }
  }

  const table = document.createElement("table");
  table.className = "data-table";

  const thead = document.createElement("thead");
  const headRow = document.createElement("tr");
  for (const col of cols) {
    const th = document.createElement("th");
    th.textContent = col;
    headRow.appendChild(th);
  }
  thead.appendChild(headRow);
  table.appendChild(thead);

  const tbody = document.createElement("tbody");
  for (const row of rows) {
    const tr = document.createElement("tr");
    for (const col of cols) {
      const td = document.createElement("td");
      const value = row[col];
      if (value !== null && typeof value === "object") {
        td.appendChild(renderAuto(value));
      } else {
        td.textContent = value === null || value === undefined ? "" : String(value);
      }
      tr.appendChild(td);
    }
    tbody.appendChild(tr);
  }
  table.appendChild(tbody);

  return table;
}
