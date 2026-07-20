// reportview.js — thin wrapper views for the projection-accuracy and
// team-value pages. Both are self-contained static HTML rendered by the Go
// `projection-site` command (internal/report, internal/valuereport) and
// published under this same CloudFront distribution's "report/" prefix, so
// they're just embedded here rather than re-implemented as API-backed views.
function renderIframe(root, src) {
  const iframe = document.createElement("iframe");
  iframe.src = src;
  iframe.style.width = "100%";
  iframe.style.height = "calc(100vh - 4rem)";
  iframe.style.border = "0";
  root.appendChild(iframe);
}

export function renderProjections(root) {
  renderIframe(root, "/report/index.html");
}

export function renderValue(root) {
  renderIframe(root, "/report/value.html");
}
