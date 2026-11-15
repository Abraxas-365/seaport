import { test, expect } from "@playwright/test"

// iam mode (`TestUIMembers` in internal/bootstrap/ui_members_test.go): the port
// runs against a real IAMKit and each spec starts from a session the Go side
// signed in. Skipped in the token-mode run, which has no IAMKit.
const admin = process.env.SEAPORT_UI_MEMBERS_ADMIN_STATE
const viewer = process.env.SEAPORT_UI_MEMBERS_VIEWER_STATE
test.skip(!admin || !viewer, "run through `SEAPORT_TEST_UI=1 SEAPORT_TEST_IAMKIT=1 go test -run TestUIMembers ./internal/bootstrap/`")

test.describe("an administrator of the organization", () => {
  test.use({ storageState: admin })

  test("invites, sees the link, issues an API key once and revokes it", async ({ page }) => {
    await page.goto("/")
    await page.getByRole("link", { name: "Miembros y accesos" }).click()
    await expect(page.getByRole("heading", { name: "Miembros y accesos" })).toBeVisible()
    // The members tab lists the signed-in person, read-only.
    await expect(page.getByText("Tú")).toBeVisible()

    // Invite: no email delivery is configured, so the dialog hands over the link.
    await page.getByRole("tab", { name: "Invitaciones" }).click()
    await page.getByRole("button", { name: "Invitar" }).first().click()
    await page.getByLabel("Correo").fill("nueva@example.com")
    await page.getByRole("button", { name: "Invitar", exact: true }).click()
    await expect(page.getByTestId("invitation-link")).toContainText("token=")
    await page.getByRole("button", { name: "Listo" }).click()
    await expect(page.getByText("nueva@example.com")).toBeVisible()
    await expect(page.getByText("Pendiente")).toBeVisible()

    // Revoke it again.
    await page.getByRole("button", { name: /Revocar la invitación a nueva@example.com/ }).click()
    await page.getByRole("button", { name: "Revocar", exact: true }).click()
    await expect(page.getByText("Sin invitaciones pendientes")).toBeVisible()

    // An API key: the value shows once.
    await page.getByRole("tab", { name: "Llaves de API" }).click()
    await page.getByRole("button", { name: /Nueva llave/ }).first().click()
    await page.getByLabel("Nombre").fill("ci")
    await page.getByRole("button", { name: "Crear llave" }).click()
    await expect(page.getByTestId("issued-api-key")).toContainText("ik_key_")
    await page.getByRole("button", { name: "Listo" }).click()
    await expect(page.getByTestId("issued-api-key")).toHaveCount(0)
    await expect(page.getByText("ci", { exact: true })).toBeVisible()
    await page.getByRole("button", { name: /Revocar la llave ci/ }).click()
    await page.getByRole("button", { name: "Revocar", exact: true }).click()
    await expect(page.getByText("Revocada")).toBeVisible()
  })
})

test.describe("a person without the members role", () => {
  test.use({ storageState: viewer })

  test("has no menu item and no page", async ({ page }) => {
    await page.goto("/")
    await expect(page.getByRole("link", { name: "Muelles" })).toBeVisible()
    await expect(page.getByRole("link", { name: "Miembros y accesos" })).toHaveCount(0)
    await page.goto("/members")
    await expect(page.getByText("Sin permiso")).toBeVisible()
  })
})
