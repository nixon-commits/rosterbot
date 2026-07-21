// jobs.js — renders one card per allowlisted job from GET /v1/jobs, with a
// form built from each job's declared Param schema. Confirmation before
// firing is driven entirely by the job's `mutating` flag from the API — never
// hardcode which jobs are "risky" here.
import { api, ApiError } from "./api.js";
import { escapeHtml } from "./render.js";
import { watchRun } from "./live.js";

export async function renderJobs(root) {
  root.innerHTML = "<p class=\"muted\">Loading jobs…</p>";
  let jobs;
  try {
    const resp = await api.jobs();
    jobs = resp.jobs;
  } catch (err) {
    root.innerHTML = "";
    root.appendChild(errorCard(err));
    return;
  }
  root.innerHTML = "";
  for (const job of jobs) {
    root.appendChild(jobCard(job));
  }
}

function jobCard(job) {
  const card = document.createElement("div");
  card.className = "card";

  const title = document.createElement("h3");
  title.textContent = job.label + (job.mutating ? " ⚠" : "");
  card.appendChild(title);

  const desc = document.createElement("p");
  desc.className = "muted";
  desc.textContent = job.description;
  card.appendChild(desc);

  const form = document.createElement("form");
  form.className = "job-form";
  const inputs = {};

  for (const param of job.params) {
    const wrapper = document.createElement("div");
    const id = `${job.name}-${param.name}`;

    const label = document.createElement("label");
    label.textContent = param.help ? `${param.label} — ${param.help}` : param.label;
    label.htmlFor = id;

    let input;
    if (param.type === "bool") {
      input = document.createElement("input");
      input.type = "checkbox";
      input.checked = param.default === "true";
    } else if (param.type === "enum") {
      input = document.createElement("select");
      // A native <select> always has *some* value selected — unlike a text
      // input, it can't be "empty" — so a param with no declared default
      // (e.g. optimize's "Projection system") needs an explicit blank option.
      // Without it the form would always send the first listed option,
      // silently overriding the job's own default every time it runs.
      const blank = document.createElement("option");
      blank.value = "";
      blank.textContent = "(default)";
      input.appendChild(blank);
      for (const opt of param.options || []) {
        const o = document.createElement("option");
        o.value = opt;
        o.textContent = opt;
        if (opt === param.default) o.selected = true;
        input.appendChild(o);
      }
    } else if (param.type === "int") {
      input = document.createElement("input");
      input.type = "number";
      if (param.min != null) input.min = String(param.min);
      if (param.max != null) input.max = String(param.max);
      if (param.default) input.value = param.default;
    } else {
      input = document.createElement("input");
      input.type = "text";
      if (param.default) input.value = param.default;
      if (param.pattern) input.pattern = param.pattern;
    }
    input.id = id;
    input.name = param.name;
    inputs[param.name] = input;

    wrapper.append(label, input);
    form.appendChild(wrapper);
  }

  const submit = document.createElement("button");
  submit.type = "submit";
  submit.className = "primary";
  submit.textContent = job.mutating ? "Run (real changes)" : "Run";
  form.appendChild(submit);

  const status = document.createElement("p");
  status.className = "muted";
  form.appendChild(status);

  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    if (job.mutating && !window.confirm(`${job.label} will make real changes (Fantrax roster and/or a push notification). Continue?`)) {
      return;
    }
    const params = {};
    for (const [name, input] of Object.entries(inputs)) {
      params[name] = input.type === "checkbox" ? String(input.checked) : input.value;
    }
    submit.disabled = true;
    status.textContent = "Starting…";
    status.classList.remove("error");
    try {
      const resp = await api.triggerJob(job.name, params);
      status.textContent = `Started: ${resp.command} (run ${resp.id})`;
      // Land the user on the Runs view with the live hero already tracking
      // this run, instead of leaving them staring at a static status line.
      watchRun(resp.id);
      window.location.hash = "#runs";
    } catch (err) {
      status.textContent = errorMessage(err);
      status.classList.add("error");
    } finally {
      submit.disabled = false;
    }
  });

  card.appendChild(form);
  return card;
}

function errorMessage(err) {
  if (err instanceof ApiError && err.status === 409) return "Already running — check the Runs tab.";
  if (err instanceof ApiError && err.status === 501) return "Job triggering isn't available on this server (local dev has no ECS).";
  return "Failed: " + err.message;
}

function errorCard(err) {
  const card = document.createElement("div");
  card.className = "card";
  card.innerHTML = `<p class="error">Failed to load jobs: ${escapeHtml(err.message)}</p>`;
  return card;
}
