import { expect, type Locator, type Page } from "@playwright/test"

/**
 * Page objects: what a person does in the admin app, in the app's own
 * (Spanish) words. Specs assert what people see, never CSS or internals.
 */
export class Pages {
  readonly docks: DocksPage
  readonly dock: DockPage
  readonly conversation: ConversationPage
  readonly agents: AgentsPage
  readonly customs: CustomsPage
  readonly vaults: VaultsPage

  constructor(readonly page: Page) {
    this.docks = new DocksPage(page)
    this.dock = new DockPage(page)
    this.conversation = new ConversationPage(page)
    this.agents = new AgentsPage(page)
    this.customs = new CustomsPage(page)
    this.vaults = new VaultsPage(page)
  }
}

export class DocksPage {
  constructor(private readonly page: Page) {}

  async open() {
    await this.page.goto("/docks")
    await expect(this.page.getByRole("heading", { name: "Muelles" })).toBeVisible()
  }

  /** "Nuevo muelle": picks the channel card by its label, names the dock and creates it. */
  async create(channel: string, name: string) {
    await this.page.getByRole("button", { name: "Nuevo muelle" }).first().click()
    const dialog = this.page.getByRole("dialog", { name: "Nuevo muelle" })
    await dialog.getByRole("radio", { name: new RegExp(`^${channel}`) }).click()
    await dialog.getByLabel("Nombre").fill(name)
    await dialog.getByRole("button", { name: /^Crear/ }).click()
  }

  card(name: string): Locator {
    // Scoped to the page: the sidebar lists the docks too.
    return this.page.getByRole("main").getByRole("link", { name: new RegExp(name) })
  }
}

export class DockPage {
  constructor(private readonly page: Page) {}

  async open(id: string) {
    await this.page.goto(`/docks/${id}`)
  }

  title(name: string): Locator {
    return this.page.getByRole("heading", { level: 1, name })
  }

  /** The conversations card of a dock page. */
  conversationLink(title: string): Locator {
    // The row's accessible name starts with the title; "Copiar …" buttons never do.
    return this.page.getByRole("button", { name: new RegExp(`^${title}\\b`) })
  }
}

export class ConversationPage {
  constructor(private readonly page: Page) {}

  async open(id: string) {
    await this.page.goto(`/inbox/${id}`)
  }

  /** A message bubble holding the text; `in` came from people, `out` was sent by the port. */
  bubble(text: string | RegExp, direction: "in" | "out"): Locator {
    return this.page.locator(`[data-slot="ship-bubble"][data-direction="${direction}"]`).filter({ hasText: text })
  }

  composer(): Locator {
    return this.page.getByRole("textbox").last()
  }

  async send(text: string) {
    await this.composer().fill(text)
    await this.page.getByRole("button", { name: "Enviar" }).click()
  }

  turnSummary(): Locator {
    return this.page.locator('[data-slot="turn-summary"]')
  }

  approvalCard(): Locator {
    return this.page.locator('[data-slot="approval-card"]')
  }

  /** The schedules block of the details sheet. */
  async openDetails() {
    await this.page.getByRole("button", { name: "Detalles" }).click()
    return this.page.getByRole("dialog")
  }
}

export class AgentsPage {
  constructor(private readonly page: Page) {}

  /** An agent's page, on a tab when `tab` names one ("configuracion", "secretos", "donde"). */
  async open(id: string, tab?: string) {
    await this.page.goto(`/agents/${id}${tab ? `#${tab}` : ""}`)
  }

  async list() {
    await this.page.goto("/agents")
    await expect(this.page.getByRole("heading", { level: 1, name: "Agentes" })).toBeVisible()
  }

  /** An agent's card on the list. */
  card(name: string): Locator {
    return this.page.getByRole("main").locator('[data-slot="card"]').filter({ hasText: `@${name}` })
  }

  /**
   * "Nuevo agente" on the list: name, where it runs, harness and, hosted,
   * a model key (`key`) or the Claude account (no key). Ends on its page.
   */
  async create(
    name: string,
    opts: { where: "hosted" | "remote"; harness: "rness" | "Claude Code" | "none"; key?: string },
  ) {
    await this.page.getByRole("button", { name: "Nuevo agente" }).first().click()
    const dialog = this.page.getByRole("dialog", { name: "Nuevo agente" })
    await dialog.getByLabel("Nombre").fill(name)
    await dialog.getByRole("button", { name: "Siguiente" }).click()
    await dialog
      .getByRole("radio", { name: opts.where === "hosted" ? /En el puerto/ : /Donde alguien lo conecte/ })
      .click()
    await dialog.getByRole("button", { name: "Siguiente" }).click()
    await dialog
      .getByRole("radio", { name: opts.harness === "none" ? /Lo elige quien conecta/ : new RegExp(`^${opts.harness}`) })
      .click()
    if (opts.where === "hosted") {
      await dialog.getByRole("button", { name: "Siguiente" }).click()
      if (opts.key) {
        await dialog.getByRole("radio", { name: /Clave de API/ }).click()
        await dialog.getByLabel(/^Clave de /).fill(opts.key)
      } else await dialog.getByRole("radio", { name: /Cuenta de Claude/ }).click()
    }
    await dialog.getByRole("button", { name: "Crear agente" }).click()
    await expect(dialog).toHaveCount(0)
    await expect(this.page.getByRole("heading", { level: 1, name: `@${name}` })).toBeVisible()
  }

  /** The "Guardar vN" button of the config editor. */
  save(): Locator {
    return this.page.getByRole("button", { name: /^Guardar v\d+$/ })
  }
}

export class CustomsPage {
  constructor(private readonly page: Page) {}

  async open() {
    await this.page.goto("/customs")
    await expect(this.page.getByRole("heading", { name: "Aduana" })).toBeVisible()
  }
}

export class VaultsPage {
  constructor(private readonly page: Page) {}

  async open() {
    await this.page.goto("/vaults")
    await expect(this.page.getByRole("heading", { name: "Bóvedas" })).toBeVisible()
  }

  /** "Nueva bóveda": names it and creates it. */
  async create(name: string, title: string) {
    await this.page.getByRole("button", { name: "Nueva bóveda" }).first().click()
    const dialog = this.page.getByRole("dialog", { name: "Nueva bóveda" })
    await dialog.getByLabel("Nombre").fill(name)
    await dialog.getByLabel("Título").fill(title)
    await dialog.getByRole("button", { name: "Crear" }).click()
  }

  /** The vault's row in the list (a link to its page). */
  card(name: string): Locator {
    return this.page.getByRole("main").getByRole("link", { name: new RegExp(name) })
  }

  /** "Nueva entrada" on a vault's page. */
  async createEntry(name: string, body: string) {
    await this.page.getByRole("button", { name: "Nueva entrada" }).click()
    const dialog = this.page.getByRole("dialog", { name: "Nueva entrada" })
    await dialog.getByLabel("Nombre").fill(name)
    await dialog.getByLabel("Contenido").fill(body)
    await dialog.getByRole("button", { name: "Crear" }).click()
  }

  /** An entry in the list of the vault's page. */
  entry(name: string): Locator {
    return this.page.getByRole("main").getByRole("button", { name: new RegExp(`^${name}\\b`) })
  }
}
