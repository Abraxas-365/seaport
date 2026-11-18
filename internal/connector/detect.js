// seaportDetect reads a page and says what only a person can do on it:
// "login" (a sign-in form or a known sign-in host), "captcha" (a challenge
// widget or wall) or "2fa" (a one-time code), else "", and refused: true on
// a sign-in page that refuses automated browsers. It writes nothing to
// the page. The connector runs it over CDP (Runtime.evaluate) on every
// loaded page of the agent's browser; the web's Vitest suite runs it under
// jsdom with fixture pages (web/src/lib/browser-detect.test.ts).
//
// Kept as a plain script (no modules, no modern syntax the page's CSP or
// an old engine could refuse): it is evaluated as-is.
(function () {
  "use strict";

  // Hosts whose pages are sign-ins by definition (a match is the host or
  // one of its subdomains).
  var LOGIN_HOSTS = [
    "accounts.google.com",
    "login.microsoftonline.com",
    "login.live.com",
    "login.microsoft.com",
    "appleid.apple.com",
    "idmsa.apple.com",
    "github.com/login",
    "auth.atlassian.com",
    "id.atlassian.com",
    "login.salesforce.com",
    "signin.aws.amazon.com",
    "login.okta.com",
    "okta.com",
    "auth0.com",
    "login.yahoo.com",
    "slack.com/signin",
    "accounts.zoho.com",
    "login.xero.com",
    "secure.login.gov",
  ];

  // Substrings of a frame or script URL that mean a challenge widget.
  var CAPTCHA_SOURCES = [
    "google.com/recaptcha",
    "recaptcha.net/recaptcha",
    "hcaptcha.com",
    "challenges.cloudflare.com",
    "arkoselabs.com",
    "funcaptcha.com",
    "captcha-delivery.com", // DataDome
    "geo.captcha-delivery.com",
    "px-cdn.net", // PerimeterX
    "px-cloud.net",
    "perimeterx.net",
    "geetest.com",
    "awswaf.com",
  ];

  // Selectors that mean a challenge wall is the page itself.
  var CAPTCHA_WALLS = [
    "#challenge-form",
    "#challenge-running",
    "#cf-challenge-running",
    ".cf-browser-verification",
    "#captcha-delivery",
    "#px-captcha",
    "#g-recaptcha",
    ".g-recaptcha",
    ".h-captcha",
    ".cf-turnstile",
    "iframe[title*='recaptcha' i]",
    "iframe[title*='hcaptcha' i]",
    "iframe[title*='challenge' i]",
    "[data-sitekey]",
  ];

  // Pages where an identity provider refuses to sign anyone in because the
  // browser is automated (Google's "Couldn't sign you in · This browser or
  // app may not be secure"). No person can pass them in this browser, so
  // they are not a takeover: the page is reported as refused and the agent
  // is told to use the site's own sign-in or an API instead. A match is the
  // host (or a subdomain) and a path containing the marker.
  var REFUSED_PAGES = [
    { host: "accounts.google.com", path: "rejected" },
  ];

  function refused(host, path) {
    for (var i = 0; i < REFUSED_PAGES.length; i++) {
      var r = REFUSED_PAGES[i];
      if ((host === r.host || endsWithDot(host, r.host)) && path.indexOf(r.path) >= 0) return true;
    }
    return false;
  }

  function hostMatches(host, path, entry) {
    var slash = entry.indexOf("/");
    if (slash >= 0) {
      var h = entry.slice(0, slash);
      var p = entry.slice(slash);
      return (host === h || endsWithDot(host, h)) && path.indexOf(p) === 0;
    }
    return host === entry || endsWithDot(host, entry);
  }

  function endsWithDot(host, suffix) {
    var tail = "." + suffix;
    return host.length > tail.length && host.slice(-tail.length) === tail;
  }

  // visible: in the layout and not hidden. jsdom has no layout
  // (getClientRects is empty), so an element with no rect but no
  // hidden/display:none/type=hidden counts as visible there.
  function visible(el) {
    if (!el || el.hidden) return false;
    if (el.getAttribute("type") === "hidden") return false;
    var node = el;
    var view = el.ownerDocument && el.ownerDocument.defaultView;
    while (node && node.nodeType === 1) {
      if (node.hidden) return false;
      var inline = node.style;
      if (inline && (inline.display === "none" || inline.visibility === "hidden")) return false;
      if (view && typeof view.getComputedStyle === "function") {
        var cs = view.getComputedStyle(node);
        if (cs && (cs.display === "none" || cs.visibility === "hidden")) return false;
      }
      node = node.parentNode;
    }
    return true;
  }

  function any(doc, selector, test) {
    var list;
    try {
      list = doc.querySelectorAll(selector);
    } catch (e) {
      return false;
    }
    for (var i = 0; i < list.length; i++) {
      if (!test || test(list[i])) return true;
    }
    return false;
  }

  function hasCaptcha(doc) {
    if (any(doc, "iframe[src], script[src]", function (el) {
      var src = (el.getAttribute("src") || "").toLowerCase();
      for (var i = 0; i < CAPTCHA_SOURCES.length; i++) {
        if (src.indexOf(CAPTCHA_SOURCES[i]) >= 0) return true;
      }
      return false;
    })) return true;
    for (var j = 0; j < CAPTCHA_WALLS.length; j++) {
      if (any(doc, CAPTCHA_WALLS[j], visible)) return true;
    }
    return false;
  }

  function hasOneTimeCode(doc) {
    if (any(doc, "input[autocomplete='one-time-code' i]", visible)) return true;
    // A row of single-digit boxes (6 inputs with maxlength 1, numeric).
    var boxes = 0;
    var list = doc.querySelectorAll("input[maxlength='1'][inputmode='numeric'], input[maxlength='1'][type='tel'], input[maxlength='1'][pattern*='0-9']");
    for (var i = 0; i < list.length; i++) if (visible(list[i])) boxes++;
    return boxes >= 4 && boxes <= 8;
  }

  function hasLoginForm(doc) {
    return any(doc, "input[type='password' i]", visible);
  }

  function detect(doc, loc) {
    var host = String((loc && loc.hostname) || "").toLowerCase();
    var path = String((loc && loc.pathname) || "/");
    var proto = String((loc && loc.protocol) || "");
    var out = { host: host, path: path, kind: "", refused: false };
    if (!host || (proto !== "http:" && proto !== "https:")) return out;
    if (refused(host, path)) {
      out.refused = true;
      return out;
    }
    if (hasOneTimeCode(doc)) {
      out.kind = "2fa";
      return out;
    }
    if (hasLoginForm(doc)) {
      out.kind = "login";
      return out;
    }
    for (var i = 0; i < LOGIN_HOSTS.length; i++) {
      if (hostMatches(host, path, LOGIN_HOSTS[i])) {
        out.kind = "login";
        return out;
      }
    }
    if (hasCaptcha(doc)) out.kind = "captcha";
    return out;
  }

  var root = typeof globalThis !== "undefined" ? globalThis : this;
  root.seaportDetect = detect;
  if (typeof module !== "undefined" && module.exports) module.exports = detect;
  if (typeof document !== "undefined" && typeof location !== "undefined") {
    return detect(document, location);
  }
  return null;
})();
