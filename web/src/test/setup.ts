import "@testing-library/jest-dom/vitest"
import { cleanup } from "@testing-library/react"
import { afterEach } from "vitest"

// Files that opt into the node environment (`@vitest-environment node`) have no DOM.
if (typeof window !== "undefined") {
  afterEach(() => cleanup())

  // jsdom has no ResizeObserver / matchMedia / scrollIntoView, which Radix and
  // a few components call.
  class ResizeObserverStub {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  globalThis.ResizeObserver ??= ResizeObserverStub as unknown as typeof ResizeObserver
  window.matchMedia ??= ((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  })) as typeof window.matchMedia
  Element.prototype.scrollIntoView ??= () => {}
}
