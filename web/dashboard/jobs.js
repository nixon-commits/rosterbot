// jobs.js — one collapsed drawer per allowlisted job from GET /v1/jobs, with a
// form built from each job's declared Param schema.
//
// Two rules that must not be relaxed:
//
//  1. Confirmation before firing is driven entirely by the job's `mutating`
//     flag from the API. Never hardcode which jobs are "risky" here — the
//     server owns that, and a client-side list would drift the moment a job's
//     behaviour changes.
//  2. Params are rendered from the declared `type`. A type this file does not
//     recognise falls back to a text input rather than being skipped, so a
//     server-side addition degrades to "typable" instead of "invisible" —
//     silently dropping a field would submit the job without it.
//
// Jobs render collapsed because the tab is a launcher, not a dashboard: with
// every form expanded, fourteen jobs ran to roughly 5000px and finding the one
// you came for meant scrolling past the rest.
import { api, ApiError } from "./api.js";
import { watchRun } from "./live.js";

const el = (tag, cls, text) => {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text !== undefined) n.textContent = text;
  return n;
};

export async function renderJobs(root) {
  root.replaceChildren(el("p", "muted", "Loading jobs…"));
  let jobs;
  try {
    const resp = await api.jobs();
    jobs = resp.jobs;
  } catch (err) {
    root.replaceChildren(errorCard(err));
    return;
  }
  root.replaceChildren();

  root.appendChild(el("p", "sub muted",
    `${jobs.length} job${jobs.length === 1 ? "" : "s"} · open one to set options and run it. ` +
    `Jobs marked ⚠ change your roster or send a push.`));

  const list = el("div", "job-list");
  for (const job of jobs) list.appendChild(jobDrawer(job));
  root.appendChild(list);
}

function jobDrawer(job) {
  const drawer = el("details", "drawer job-drawer");

  const summary = el("summary");
  const head = el("h3");
  head.append(job.label);
  if (job.mutating) head.append(" ", el("span", "badge badge-failed", "⚠ changes state"));
  summary.appendChild(head);
  drawer.appendChild(summary);

  drawer.appendChild(el("p", "sub muted", job.description));
  drawer.appendChild(jobForm(job));
  return drawer;
}

// dateInput builds a native picker. `min`/`max` are deliberately not set: the
// season range is not known here, and a wrong bound would block a legitimate
// date more often than it would prevent a typo.
function dateInput(id, value) {
  const input = el("input");
  input.type = "date";
  input.id = id;
  if (value) input.value = value;
  return input;
}

// A daterange is two pickers composing into the "start:end" string the CLI
// takes. An end left empty submits the start alone, which the backend accepts
// as a single day — so "just this date" needs no separate control.
function dateRangeField(id, param) {
  const wrap = el("div", "date-range");
  const start = dateInput(`${id}-start`, param.default);
  const end = dateInput(`${id}-end`, "");
  start.setAttribute("aria-label", `${param.label} — start`);
  end.setAttribute("aria-label", `${param.label} — end (optional)`);
  wrap.append(start, el("span", "date-range-sep", "to"), end);

  return {
    node: wrap,
    focusable: start,
    read() {
      const s = start.value.trim();
      const e = end.value.trim();
      if (!s) return "";
      return e ? `${s}:${e}` : s;
    },
    // An end date with no start is the one combination the composed string
    // cannot express; flagging it beats silently sending nothing.
    validate() {
      if (!start.value && end.value) return `${param.label}: pick a start date too.`;
      return "";
    },
  };
}

// buildField returns a uniform handle per param so the submit path never
// branches on type again.
function buildField(job, param) {
  const id = `${job.name}-${param.name}`;
  const wrapper = el("div", "job-field");

  const label = el("label", null, param.label);
  label.htmlFor = id;
  wrapper.appendChild(label);

  let field;
  if (param.type === "daterange") {
    field = dateRangeField(id, param);
    field.focusable.id = id;
    wrapper.appendChild(field.node);
  } else if (param.type === "date") {
    const input = dateInput(id, param.default);
    field = { node: input, read: () => input.value.trim(), validate: () => "" };
    wrapper.appendChild(input);
  } else if (param.type === "bool") {
    const input = el("input");
    input.type = "checkbox";
    input.id = id;
    input.checked = param.default === "true";
    // The checkbox sits beside its label rather than under it; a lone switch
    // stacked below a heading reads as a section rather than a field.
    wrapper.classList.add("job-field-inline");
    wrapper.replaceChildren(input, label);
    field = { node: input, read: () => String(input.checked), validate: () => "" };
  } else if (param.type === "enum") {
    const input = el("select");
    input.id = id;
    // A native <select> always has some value selected — unlike a text input it
    // cannot be empty — so a param with no declared default needs an explicit
    // blank option. Without it the form would always send the first listed
    // option, silently overriding the job's own default every time it runs.
    const blank = el("option", null, "(default)");
    blank.value = "";
    input.appendChild(blank);
    for (const opt of param.options || []) {
      const o = el("option", null, opt);
      o.value = opt;
      if (opt === param.default) o.selected = true;
      input.appendChild(o);
    }
    field = { node: input, read: () => input.value, validate: () => "" };
    wrapper.appendChild(input);
  } else {
    const input = el("input");
    input.id = id;
    if (param.type === "int") {
      input.type = "number";
      if (param.min != null) input.min = String(param.min);
      if (param.max != null) input.max = String(param.max);
    } else {
      input.type = "text";
      if (param.pattern) input.pattern = param.pattern;
    }
    if (param.default) input.value = param.default;
    field = { node: input, read: () => input.value.trim(), validate: () => "" };
    wrapper.appendChild(input);
  }

  if (param.help) {
    const hint = el("small", "field-hint", param.help);
    hint.id = `${id}-hint`;
    (field.focusable || field.node).setAttribute("aria-describedby", hint.id);
    wrapper.appendChild(hint);
  }

  return { name: param.name, wrapper, ...field };
}

function jobForm(job) {
  const form = el("form", "job-form");
  const fields = (job.params || []).map((p) => buildField(job, p));
  for (const f of fields) form.appendChild(f.wrapper);

  const actions = el("div", "job-actions");
  const submit = el("button", "primary", job.mutating ? "Run (real changes)" : "Run");
  submit.type = "submit";
  actions.appendChild(submit);
  const status = el("p", "muted job-status");
  actions.appendChild(status);
  form.appendChild(actions);

  form.addEventListener("submit", async (e) => {
    e.preventDefault();

    // Client-side checks catch only what the form itself can know is wrong.
    // Everything else is the server's call — it owns the allowlist and the
    // real validation, and duplicating those rules here would let the two
    // drift.
    for (const f of fields) {
      const problem = f.validate();
      if (problem) {
        status.textContent = problem;
        status.classList.add("error");
        return;
      }
    }

    if (job.mutating && !window.confirm(
      `${job.label} will make real changes (Fantrax roster and/or a push notification). Continue?`)) {
      return;
    }

    const params = {};
    for (const f of fields) params[f.name] = f.read();

    submit.disabled = true;
    status.textContent = "Starting…";
    status.classList.remove("error");
    try {
      const resp = await api.triggerJob(job.name, params);
      status.textContent = `Started: ${resp.command}`;
      // Land on Runs with the live hero already tracking this run, instead of
      // leaving the user staring at a static status line.
      watchRun(resp.id);
      window.location.hash = "#runs";
    } catch (err) {
      status.textContent = errorMessage(err);
      status.classList.add("error");
    } finally {
      submit.disabled = false;
    }
  });

  return form;
}

function errorMessage(err) {
  if (err instanceof ApiError && err.status === 409) return "Already running — check the Runs tab.";
  if (err instanceof ApiError && err.status === 501) return "Job triggering isn't available on this server (local dev has no ECS).";
  if (err instanceof ApiError && err.status === 400) return err.message;
  return "Failed: " + err.message;
}

function errorCard(err) {
  const card = el("div", "card");
  card.appendChild(el("p", "error", `Failed to load jobs: ${err.message}`));
  return card;
}
