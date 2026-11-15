import { describe, expect, it } from "vitest"

import { cronPresets, describeCron } from "./cron"

describe("describeCron", () => {
  it.each([
    ["*/5 * * * *", "Cada 5 minutos"],
    ["*/1 * * * *", "Cada minuto"],
    ["0 * * * *", "Cada hora"],
    ["30 * * * *", "Cada hora, al minuto 30"],
    ["0 */6 * * *", "Cada 6 horas"],
    ["0 9 * * *", "Todos los días a las 09:00"],
    ["5 18 * * *", "Todos los días a las 18:05"],
    ["0 9 * * 1-5", "De lunes a viernes a las 09:00"],
    ["0 9 * * mon-fri", "De lunes a viernes a las 09:00"],
    ["0 10 * * 0,6", "Sábados y domingos a las 10:00"],
    ["0 9 * * 1", "Los lunes a las 09:00"],
    ["0 9 * * 7", "Los domingos a las 09:00"],
    ["0 9 * * 1,3,5", "Los lunes, miércoles, viernes a las 09:00"],
    ["0 8 15 * *", "El día 15 de cada mes a las 08:00"],
  ])("%s → %s", (cron, words) => {
    expect(describeCron(cron)).toBe(words)
  })

  it("reads the macros the server accepts", () => {
    expect(describeCron("@hourly")).toBe("Cada hora")
    expect(describeCron("@daily")).toBe("Todos los días a las 00:00")
    expect(describeCron("@weekly")).toBe("Los domingos a las 00:00")
    expect(describeCron("@monthly")).toBe("El día 1 de cada mes a las 00:00")
    expect(describeCron("  @DAILY ")).toBe("Todos los días a las 00:00")
  })

  it.each(["0 9 * 1 *", "0 9 1-15 * *", "0,30 9 * * *", "* * * * *", "0 9 * *", "not a cron", ""])(
    "shows an uncommon shape as it is: %j",
    (cron) => {
      expect(describeCron(cron)).toBe(cron)
    },
  )

  it("describes every preset it offers", () => {
    for (const p of cronPresets) expect(describeCron(p.cron), p.label).not.toBe(p.cron)
  })
})
