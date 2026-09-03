// connstate.js — the tenant-facing vocabulary for a Fantrax connection.
//
// One home, because two surfaces now render it: Settings (the current state of
// your connection) and the Runs tab (what one connect RUN concluded, beside a
// ledger status that deliberately reads SUCCESS on tenant-actionable failures —
// rosterbot-jg92). Two copies of these strings would let the two screens
// disagree about what went wrong, which is the same failure one level up from
// the one this bead fixes.
//
// cmd/connect_feed.go's connectFailureMessage mirrors FAILURE_COPY for the
// activity feed and for push. It is a deliberate third copy in another
// language, not an oversight.

// CONNECTION_COPY is what each state means to the person reading it, not what
// it means to the system. "needs_reconnect" is a status; "your saved password
// no longer works" is an answer.
export const CONNECTION_COPY = {
  verified: ["Connected", "badge-ok"],
  // Bounded, not open-ended (rosterbot-spb9): a connect task that crashes
  // before it writes ANY record leaves this status showing forever otherwise
  // — 10 minutes is connectInFlightWindow in
  // internal/lineupapi/connect.go, the same value that unblocks a fresh
  // submission.
  pending: ["Checking your credentials… (should resolve within 10 minutes)", "badge-info"],
  needs_reconnect: ["Not connected — your saved credentials no longer work", "badge-failed"],
  // badge-info, not badge-failed: nothing on the tenant's side failed. Fantrax
  // accepted the sign-in and a step after it did not finish (rosterbot-ch0s).
  interrupted: ["Not connected — the last check did not finish", "badge-info"],
  // Also badge-info, and for the same reason one step earlier: the check
  // never reached Fantrax at all, so there is nothing here about the
  // tenant's credentials in either direction (rosterbot-spb9).
  check_failed: ["Not connected — the last check could not run", "badge-info"],
};

// FAILURE_COPY translates a ConnErr class into something actionable. The
// classes exist precisely so a user can be told which of these happened rather
// than "connection failed"; leaving them untranslated here would give that away
// at the last step.
export const FAILURE_COPY = {
  bad_credentials: "Fantrax rejected that username or password.",
  two_factor_required:
    "Fantrax asked for a two-factor code. rosterbot cannot answer one, so this " +
    "account can only be connected with two-factor auth turned off.",
  bot_challenge:
    "Fantrax blocked the sign-in as automated traffic. This one is not your " +
    "fault and not something you can fix — it has been reported.",
  login_challenge_or_timeout:
    "The sign-in did not complete and Fantrax did not say why. Trying again " +
    "sometimes works.",
  team_not_owned: "Those credentials do not control the team you were invited for.",
  no_team:
    "Your Fantrax sign-in worked. What's missing is a team: this account has " +
    "no Fantrax team assigned, and only an admin can assign one — re-entering " +
    "your password won't change this.",
  team_claimed: "That Fantrax team is already claimed by another account.",
  verification_interrupted:
    "Your Fantrax sign-in worked — your password is not the problem. Something " +
    "after it did not finish, so the connection is not confirmed yet. Try " +
    "connecting again in a minute.",
  check_failed:
    "The check couldn't run because of a problem on our side — your password " +
    "is not the problem. Try connecting again in a minute.",
};

// CONNECT_CHIP is the TERSE form for the runs table, where the sentence in
// FAILURE_COPY does not fit. It becomes the chip text; FAILURE_COPY becomes its
// title.
//
// An unknown class falls back to the raw class rather than to nothing — degrade
// to noise, never to silence. That fallback is what makes a class added to
// internal/lineupapi/credentials.go without a matching entry here render
// "connection failed: some_new_class" instead of an unexplained red chip.
export const CONNECT_CHIP = {
  bad_credentials: "sign-in rejected",
  two_factor_required: "two-factor required",
  bot_challenge: "blocked as a bot",
  login_challenge_or_timeout: "sign-in did not complete",
  team_not_owned: "team not owned",
  no_team: "no team assigned",
  team_claimed: "team already claimed",
  verification_interrupted: "sign-in worked, check did not finish",
  check_failed: "check never reached fantrax",
};
