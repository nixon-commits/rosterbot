package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestDashboardFunction_BehavesCorrectly RUNS the viewer-request function.
//
// Every other assertion about this function pattern-matches its SOURCE: the
// allowlist parses, the redirect target is the apex, the source says
// statusCode: 301. None of them executes a line of it, so the logic that
// actually decides what a user gets — host case-folding, the multiValue loop,
// the query reconstruction, the /invite rewrite — was covered by nothing. An
// edit that inverted the allowlist test, dropped the '?' prefix, or broke out
// of the multiValue loop early would leave every source-shaped assertion
// passing and surface in production as an invite link that lost its token,
// i.e. as a tester who cannot enrol.
//
// The function is a Go string, so the only way to execute it is to hand it to a
// JavaScript engine. node is the one already pinned in buildspec.yml
// (runtime-versions nodejs: 22), so this runs in CI rather than only locally.
// CloudFront Functions run a JS 2.0 runtime rather than node, but nothing here
// uses a node built-in — the function is ES5-level syntax over the event
// object, and what is being pinned is its branching, not its host.
func TestDashboardFunction_BehavesCorrectly(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; skipping the executable check of the viewer-request function " +
			"(CI pins nodejs 22 in buildspec.yml, so this does run there)")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "check.js")
	source := dashboardFunctionCode(t) + "\n" + fmt.Sprintf(driverJS, apexHost, dashHost)
	if err := os.WriteFile(script, []byte(source), 0o644); err != nil {
		t.Fatalf("write driver: %v", err)
	}

	out, err := exec.Command(node, script).CombinedOutput()
	if err != nil {
		t.Fatalf("the viewer-request function does not behave as intended:\n%s", out)
	}
}

// driverJS is appended after the function source. %s is the apex, then dash.
const driverJS = `
var APEX = '%s';
var DASH = '%s';
var DEPRECATED = 'd111111abcdef8.cloudfront.net';

function ev(host, uri, qs) {
    var headers = {};
    if (host !== null) { headers.host = { value: host }; }
    return { request: { method: 'GET', uri: uri, querystring: qs || {}, headers: headers } };
}
function loc(res) { return res.headers && res.headers.location && res.headers.location.value; }

var failures = [];
function check(name, ok, detail) { if (!ok) { failures.push(name + ' -- ' + detail); } }

// --- the deprecated hostname redirects, preserving path and query ---
var r = handler(ev(DEPRECATED, '/invite', { token: { value: 'aBc-_123' } }));
check('invite link survives the redirect', loc(r) === 'https://' + APEX + '/invite?token=aBc-_123',
    'location was ' + loc(r) + '; a stale invite link must keep its token or the invitee cannot enrol');
check('redirect is permanent', r.statusCode === 301, 'statusCode was ' + r.statusCode);
check('redirect is bounded', r.headers['cache-control'].value === 'max-age=3600',
    'cache-control was ' + JSON.stringify(r.headers['cache-control']));

r = handler(ev(DEPRECATED, '/', {}));
check('bare root redirects', loc(r) === 'https://' + APEX + '/',
    'location was ' + loc(r) + '; an empty querystring must not append a stray ?');

// Percent-encoded values are passed through untouched. CloudFront hands them
// to the function still encoded, so re-encoding would double-encode; this
// pins the decision documented at the reconstruction in infra.go.
r = handler(ev(DEPRECATED, '/p', { q: { value: 'a%%26b' } }));
check('encoded value round-trips', loc(r) === 'https://' + APEX + '/p?q=a%%26b',
    'location was ' + loc(r) + '; an encoded & must not be re-encoded or decoded');

// Duplicate parameters: multiValue carries every value INCLUDING the first,
// so iterating it must not also emit q.value.
r = handler(ev(DEPRECATED, '/p', { t: { value: '1', multiValue: [{ value: '1' }, { value: '2' }] } }));
check('duplicate params survive exactly once', loc(r) === 'https://' + APEX + '/p?t=1&t=2',
    'location was ' + loc(r));

// A missing Host header must redirect rather than be treated as canonical.
r = handler(ev(null, '/', {}));
check('absent host is not canonical', r.statusCode === 301, 'statusCode was ' + r.statusCode);

// --- canonical hostnames are served, never redirected ---
r = handler(ev(APEX, '/invite', { token: { value: 'x' } }));
check('apex serves /invite', r.uri === '/index.html',
    'uri was ' + r.uri + '; the universal-link fallback page must render');
check('apex does not redirect', r.statusCode === undefined, 'apex returned a redirect');

r = handler(ev(APEX.toUpperCase(), '/', {}));
check('host match is case-insensitive', r.statusCode === undefined,
    'an upper-case Host was treated as deprecated and redirected');

r = handler(ev(APEX + ':443', '/', {}));
check('host match ignores the port', r.statusCode === undefined,
    'Host with an explicit :443 was treated as deprecated; a client that preserves the port loops');

r = handler(ev(DASH, '/.well-known/apple-app-site-association', {}));
check('AASA is served unmodified', r.uri === '/.well-known/apple-app-site-association' && r.statusCode === undefined,
    'AASA was rewritten or redirected; Apple does not follow redirects when fetching it');

r = handler(ev(DASH, '/app.js', {}));
check('ordinary assets pass through', r.uri === '/app.js' && r.statusCode === undefined,
    'uri was ' + r.uri);

if (failures.length) {
    console.error(failures.join('\n'));
    process.exit(1);
}
`
