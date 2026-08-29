// leagues.js — the Sleeper league connector on the Settings page.
//
// A section module like passkeys.js rather than more of settings.js: this is a
// two-mode stateful widget (connected list / lookup-and-pick) and settings.js
// is already carrying four cards.
//
// Built with createElement/textContent throughout, following views.js,
// football.js and settings.js — NOT value.js, which predates the rule. That
// matters more here than on most pages: a league name is chosen by whoever
// created the league, so `<img src=x onerror=...>` is a name someone can
// actually register on Sleeper and hand to this renderer.
import { api, ApiError } from "./api.js";
import { el } from "./render.js";

// STATUS_COPY renders Sleeper's own documented lifecycle value.
//
// Deliberately the ONLY league classification on this page. The obvious
// companion — a "Redraft"/"Dynasty" word — would have to come from
// settings.type, which Sleeper's docs render as an empty object and never
// define. Live data on this deployment carries a type value outside the
// conventional 0/1/2 mapping entirely, so a format label would render
// confidently for every league while being unverifiable for some of them. An
// unknown status renders as nothing rather than as its raw token, for the same
// reason: a miss reads as absence, a wrong word reads as a fact.
const STATUS_COPY = {
  pre_draft: "Pre-draft",
  drafting: "Drafting",
  in_season: "In season",
  complete: "Complete",
};

// Sleeper serves avatars from its own CDN, keyed by the opaque id the league
// object carries. No CSP is set on this origin, so the image loads; the error
// handler below is for a stale id, not for a policy block.
const AVATAR_BASE = "https://sleepercdn.com/avatars/thumbs/";

export async function renderLeaguesSection(container) {
  const card = el("section", "card");
  card.append(el("h2", null, "Sleeper leagues"));
  card.append(el("p", "muted",
    "Connecting a league records it on your rosterbot account, so it follows " +
    "you to any device you sign in from. rosterbot can read Sleeper leagues " +
    "but never change them — Sleeper's public API has no write access."));

  const list = el("div", "league-list");
  const toggle = el("button", null, "Connect a league");
  toggle.type = "button";
  const picker = el("div", "league-picker");
  const status = el("p", "muted small");
  card.append(list, toggle, picker, status);
  container.append(card);

  // connectedIDs is what the picker consults to mark a row Connected. It is a
  // Set of league ids rather than the membership objects because that is the
  // only question the picker asks of it.
  let connectedIDs = new Set();
  let found = null; // { username, leagues } once a lookup has succeeded
  let pickerOpen = false;

  function initialChip(name) {
    const t = (name || "").trim();
    // Array.from, not [0]: a league named with an emoji would otherwise get
    // half a surrogate pair.
    const first = Array.from(t)[0] || "?";
    return el("span", "league-avatar league-avatar-fallback", first.toUpperCase());
  }

  function leagueBadge(avatarID, name) {
    if (!avatarID) return initialChip(name);
    const img = el("img", "league-avatar");
    img.src = AVATAR_BASE + encodeURIComponent(avatarID);
    // Decorative: the league name is right beside it, so announcing the image
    // as well would read the row twice.
    img.alt = "";
    img.loading = "lazy";
    img.width = 32;
    img.height = 32;
    // An id Sleeper no longer serves must leave a chip, not a broken-image
    // glyph where every other row has an avatar.
    img.addEventListener("error", () => img.replaceWith(initialChip(name)));
    return img;
  }

  // leagueSubtitle joins only the fields that are present. A league with no
  // status contributes nothing rather than an empty separator.
  function leagueSubtitle(lg) {
    const parts = [];
    if (lg.total_rosters) parts.push(lg.total_rosters + " teams");
    if (lg.season) parts.push(lg.season);
    if (STATUS_COPY[lg.status]) parts.push(STATUS_COPY[lg.status]);
    return parts.join(" · ");
  }

  async function refresh() {
    let data;
    try {
      data = await api.memberships();
    } catch (err) {
      list.replaceChildren(el("p", "error",
        err instanceof ApiError && err.status === 403
          ? "Sign in with a passkey to see your leagues."
          : "Could not load your leagues."));
      return;
    }
    const rows = (data.memberships || []).filter((m) => m.platform === "sleeper");
    connectedIDs = new Set(rows.map((m) => m.league_id));
    renderConnected(rows);
  }

  function renderConnected(rows) {
    list.replaceChildren();
    if (!rows.length) {
      list.append(el("p", "muted small", "No Sleeper leagues connected yet."));
      return;
    }
    for (const m of rows) list.append(connectedRow(m));
  }

  function connectedRow(m) {
    const row = el("div", "league-row");
    // No avatar and no team count on this side, and that is a decision rather
    // than an omission. A membership stores the league's id, the team that is
    // yours in it, and its name — nothing else. The picker's richer subtitle
    // comes from a live Sleeper lookup. Copying Sleeper's team count, status
    // and avatar onto the account record would put numbers on this page that
    // go stale the day the league changes, with nothing to correct them.
    row.append(initialChip(m.display_name || m.league_id));

    const text = el("div", "league-text");
    text.append(el("div", "league-name", m.display_name || m.league_id));
    if (m.added_at) {
      text.append(el("div", "muted small",
        "Added " + new Date(m.added_at).toLocaleDateString()));
    }
    row.append(text);

    const remove = el("button", "danger", "Remove");
    remove.type = "button";
    // No confirmation step. Removing is fully reversible from the picker
    // directly below, and nothing downstream consumes a membership yet — the
    // friction would cost more than the mistake.
    remove.addEventListener("click", async () => {
      remove.disabled = true;
      status.className = "muted small";
      status.textContent = "Removing " + (m.display_name || m.league_id) + "…";
      try {
        await api.deleteMembership("sleeper", m.league_id);
        status.textContent = "Removed.";
        await refresh();
        // Keep an open picker's Connected badges honest — the league just
        // removed is selectable again.
        if (pickerOpen && found) renderPicker();
      } catch {
        remove.disabled = false;
        status.className = "error";
        status.textContent = "Could not remove that league. Try again shortly.";
      }
    });
    row.append(remove);
    return row;
  }

  function closePicker() {
    pickerOpen = false;
    found = null;
    toggle.textContent = "Connect a league";
    picker.replaceChildren();
  }

  toggle.addEventListener("click", () => {
    if (pickerOpen) {
      closePicker();
      return;
    }
    pickerOpen = true;
    toggle.textContent = "Cancel";
    picker.replaceChildren(lookupForm());
  });

  function lookupError(err) {
    if (!(err instanceof ApiError)) {
      return "Could not reach rosterbot. Try again shortly.";
    }
    switch (err.status) {
      case 404:
        return "No Sleeper account with that username.";
      // 502 is specifically "we could not reach Sleeper", which the server
      // distinguishes from an account with no leagues on purpose — only one of
      // the two is worth retrying, so the copy says which one this is.
      case 502:
        return "Could not reach Sleeper just now. Try again shortly.";
      case 400:
        return "That does not look like a Sleeper username.";
      case 501:
        return "Sleeper lookup is not available in this deployment.";
      case 403:
        return "Sign in with a passkey to connect a league.";
      default:
        return "Could not look up that account. Try again shortly.";
    }
  }

  function lookupForm() {
    const form = el("form", "league-lookup");
    const input = el("input");
    input.type = "text";
    input.name = "sleeper-username";
    input.autocomplete = "username";
    input.placeholder = "Sleeper username";
    input.required = true;

    const submit = el("button", "primary", "Find my leagues");
    submit.type = "submit";
    const note = el("p", "muted small");
    form.append(input, submit, note);

    form.addEventListener("submit", async (e) => {
      e.preventDefault();
      const username = input.value.trim();
      if (!username) return;
      submit.disabled = true;
      note.className = "muted small";
      note.textContent = "Looking up " + username + "…";
      try {
        const data = await api.sleeperLeagues(username);
        found = { username, leagues: data.leagues || [] };
        renderPicker();
      } catch (err) {
        note.className = "error";
        note.textContent = lookupError(err);
        submit.disabled = false;
      }
    });
    // One keystroke from useful.
    queueMicrotask(() => input.focus());
    return form;
  }

  function renderPicker() {
    picker.replaceChildren();

    const back = el("button", "league-back", "← @" + found.username.toUpperCase());
    back.type = "button";
    back.addEventListener("click", () => {
      found = null;
      picker.replaceChildren(lookupForm());
    });
    picker.append(back);

    if (!found.leagues.length) {
      picker.append(el("p", "muted small",
        "That account is not in any NFL leagues this season."));
      return;
    }

    const boxes = [];
    for (const lg of found.leagues) {
      // Matched on league_id, never on name. Sleeper mints a NEW league id
      // each season and links the old one through previous_league_id, so a
      // name is not an identity: this account carries two distinct ids both
      // named "Mad Lux Chopped 6.9". Matching on name would mark a fresh
      // season's league as already connected because last season's is on the
      // account. The flip side is that a membership stored in one season stops
      // matching discovery in the next and stays in the connected list as an
      // entry the picker no longer offers — correct, since it records what was
      // connected, and harmless while nothing downstream reads memberships.
      const already = connectedIDs.has(lg.league_id);
      const row = el("label", "league-row league-pick");
      const box = el("input");
      box.type = "checkbox";
      // Checked AND disabled: the row states the league is already on the
      // account and offers no control that would change that here. Removing is
      // the connected list's job, one section up.
      box.checked = already;
      box.disabled = already;
      if (!already) boxes.push({ box, league: lg });
      row.append(box, leagueBadge(lg.avatar, lg.name));

      const text = el("div", "league-text");
      text.append(el("div", "league-name", lg.name));
      const sub = leagueSubtitle(lg);
      if (sub) text.append(el("div", "muted small", sub));
      row.append(text);

      if (already) row.append(el("span", "badge badge-ok", "Connected"));
      picker.append(row);
    }

    const submit = el("button", "primary");
    submit.type = "button";
    const sync = () => {
      const n = boxes.filter((b) => b.box.checked).length;
      submit.textContent = n === 1 ? "Connect 1 league" : "Connect " + n + " leagues";
      submit.disabled = n === 0;
    };
    for (const b of boxes) b.box.addEventListener("change", sync);
    sync();
    submit.addEventListener("click", () => {
      connectSelected(boxes.filter((b) => b.box.checked).map((b) => b.league), submit);
    });
    picker.append(submit);
  }

  async function connectSelected(leagues, submit) {
    submit.disabled = true;
    status.className = "muted small";
    status.textContent = "Connecting…";

    let ok = 0;
    let lastError = "";
    // Sequential, one POST per league: the API takes no batch body. Each call
    // stands alone, and leagues already committed are NOT rolled back when a
    // later one fails — faking a rollback would remove leagues the person
    // asked for because an unrelated call timed out.
    for (const lg of leagues) {
      try {
        await api.addMembership({
          platform: "sleeper",
          league_id: lg.league_id,
          // Discovery already resolved the username to the account id and
          // stamped it on every row, so recording which team is theirs costs
          // no second lookup.
          team_id: lg.team_id,
          display_name: lg.name,
        });
        ok++;
      } catch (err) {
        // A 409 is either "already on your account" or "you have reached the
        // limit". Neither is a reason to abandon the leagues still queued, and
        // the server's own message distinguishes them — so it is surfaced
        // rather than reworded into something vaguer.
        lastError = err instanceof ApiError ? err.message : "";
      }
    }

    // Refresh from the server whatever happened. The POST responses would be
    // enough on the all-succeeded path, but a 409 returns an error envelope
    // with no membership list in it — so a run where every call conflicts has
    // nothing to read, and the list would silently stay stale.
    await refresh();

    if (ok === leagues.length) {
      status.className = "muted small";
      status.textContent = ok === 1 ? "Connected." : "Connected " + ok + " leagues.";
      closePicker();
      return;
    }
    status.className = "error";
    status.textContent =
      "Connected " + ok + " of " + leagues.length + "." + (lastError ? " " + lastError : "");
    // Re-render rather than close: the rows that did land now show Connected,
    // so the picker shows what actually happened instead of the request.
    renderPicker();
  }

  await refresh();
}
