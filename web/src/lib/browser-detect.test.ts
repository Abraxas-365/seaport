/**
 * The connector's page detector (internal/connector/detect.js, embedded in
 * the Go binary and evaluated on every page the agent's browser loads) is
 * tested here, where a DOM exists: the Go side has no Chromium in CI. The
 * script is read from the repository by path, so the Go code and this test
 * always see the same file.
 */
/// <reference types="node" />
import { readFileSync } from "node:fs"
import { resolve } from "node:path"

import { describe, expect, it } from "vitest"

const source = readFileSync(resolve(__dirname, "../../../internal/connector/detect.js"), "utf8")

type Detect = (doc: Document, loc: { hostname: string; pathname: string; protocol: string }) => { host: string; path: string; kind: string }

function load(): Detect {
  // The script returns its result when a document exists (the CDP case) and
  // defines seaportDetect on the global either way.
  const scope: { seaportDetect?: Detect } = {}
  new Function("globalThis", source).call(scope, scope)
  if (!scope.seaportDetect) throw new Error("detect.js defines no seaportDetect")
  return scope.seaportDetect
}

const detect = load()

function page(html: string, loc: Partial<{ hostname: string; pathname: string; protocol: string }> = {}) {
  const doc = document.implementation.createHTMLDocument("t")
  doc.body.innerHTML = html
  return detect(doc, { hostname: "app.example.com", pathname: "/", protocol: "https:", ...loc })
}

describe("detect.js", () => {
  it("sees nothing on a plain page", () => {
    expect(page("<h1>Dashboard</h1><form><input name='q'><button>Search</button></form>").kind).toBe("")
  })

  it("flags a sign-in form by its password field", () => {
    const r = page("<form><input type='email' name='u'><input type='password' name='p'><button>Sign in</button></form>", { pathname: "/login" })
    expect(r).toEqual({ host: "app.example.com", path: "/login", kind: "login" })
  })

  it("ignores a hidden password field", () => {
    expect(page("<form><input type='password' hidden></form>").kind).toBe("")
    expect(page("<div hidden><input type='password'></div>").kind).toBe("")
    expect(page("<div style='display:none'><input type='password'></div>").kind).toBe("")
  })

  it("flags a known sign-in host, even without a form yet", () => {
    expect(page("<div>Loading…</div>", { hostname: "accounts.google.com", pathname: "/v3/signin/identifier" }).kind).toBe("login")
    expect(page("<div/>", { hostname: "login.microsoftonline.com" }).kind).toBe("login")
    expect(page("<div/>", { hostname: "example.okta.com" }).kind).toBe("login")
    expect(page("<div/>", { hostname: "github.com", pathname: "/login" }).kind).toBe("login")
    expect(page("<div/>", { hostname: "github.com", pathname: "/org/repo" }).kind).toBe("")
    expect(page("<div/>", { hostname: "notokta.com" }).kind).toBe("")
  })

  it("flags captcha widgets and walls", () => {
    expect(page("<div class='g-recaptcha' data-sitekey='k'></div>").kind).toBe("captcha")
    expect(page("<iframe src='https://www.google.com/recaptcha/api2/anchor?k=x'></iframe>").kind).toBe("captcha")
    expect(page("<script src='https://js.hcaptcha.com/1/api.js'></script><div class='h-captcha'></div>").kind).toBe("captcha")
    expect(page("<div id='challenge-running'>Checking your browser</div>").kind).toBe("captcha")
    expect(page("<iframe src='https://challenges.cloudflare.com/cdn-cgi/challenge-platform/x'></iframe>").kind).toBe("captcha")
    expect(page("<script src='https://ct.captcha-delivery.com/c.js'></script>").kind).toBe("captcha")
    expect(page("<div id='px-captcha'></div>").kind).toBe("captcha")
  })

  it("flags a one-time code and ranks it above a sign-in", () => {
    expect(page("<form><input autocomplete='one-time-code' inputmode='numeric'></form>").kind).toBe("2fa")
    const boxes = Array.from({ length: 6 }, () => "<input maxlength='1' inputmode='numeric'>").join("")
    expect(page(`<form>${boxes}</form>`).kind).toBe("2fa")
    expect(page("<form><input maxlength='1' inputmode='numeric'></form>").kind).toBe("")
    expect(page("<form><input type='password'><input autocomplete='one-time-code'></form>").kind).toBe("2fa")
  })

  it("ranks a sign-in form above a captcha on the same page", () => {
    expect(page("<form><input type='password'><div class='g-recaptcha'></div></form>").kind).toBe("login")
  })

  it("skips pages that aren't http(s)", () => {
    expect(page("<input type='password'>", { protocol: "chrome:", hostname: "newtab" }).kind).toBe("")
    expect(page("<input type='password'>", { protocol: "about:", hostname: "" }).kind).toBe("")
  })

  it("lowercases the host", () => {
    expect(page("<input type='password'>", { hostname: "App.Example.COM" }).host).toBe("app.example.com")
  })
})
