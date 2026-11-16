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
  readonly environments: EnvironmentsPage
  readonly crew: CrewPage
  readonly customs: CustomsPage
  readonly vaults: VaultsPage

  constructor(readonly page: Page) {
    this.docks = new DocksPage(page)
    this.dock = new DockPage(page)
    this.conversation = new ConversationPage(page)
    this.agents = new AgentsPage(page)
    this.environments = new EnvironmentsPage(page)
    this.crew = new CrewPage(page)
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

  async open(id: string) {
    await this.page.goto(`/agents/${id}`)
  }
}

export class EnvironmentsPage {
  constructor(private readonly page: Page) {}

  async open() {
    await this.page.goto("/environments")
    await expect(this.page.getByRole("heading", { name: "Entornos" })).toBeVisible()
  }

  /**
   * Creates an environment from the two-step dialog; `template` is the label of
   * a starting point ("Python"). With `key`, the model key is stored on the way
   * (step 2); without, the key is left for later ("Luego").
   */
  async create(name: string, template?: string, key?: { provider: string; value: string }) {
    await this.page.getByRole("button", { name: "Entorno" }).first().click()
    const step1 = this.page.getByRole("dialog", { name: "Nuevo entorno" })
    await step1.getByLabel("Nombre").fill(name)
    if (template) await step1.getByRole("radio", { name: template }).click()
    await step1.getByRole("button", { name: "Siguiente" }).click()
    const step2 = this.page.getByRole("dialog", { name: "Clave del modelo" })
    if (key) {
      await step2.getByRole("radiogroup", { name: "Proveedor" }).getByRole("radio", { name: key.provider }).click()
      await step2.getByLabel("Clave", { exact: true }).fill(key.value)
      await step2.getByRole("button", { name: "Crear con clave" }).click()
    } else {
      await step2.getByRole("button", { name: "Luego" }).click()
    }
  }
}

export class CrewPage {
  constructor(private readonly page: Page) {}

  async open() {
    await this.page.goto("/crew")
    await expect(this.page.getByRole("heading", { name: "Tripulación" })).toBeVisible()
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
