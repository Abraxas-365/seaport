// A tiny Spanish reading of the common cron shapes (the server owns the
// real parser; anything unusual is shown as the expression itself).

const plural = ["domingos", "lunes", "martes", "miércoles", "jueves", "viernes", "sábados"]

const macros: Record<string, string> = {
  "@hourly": "0 * * * *",
  "@daily": "0 0 * * *",
  "@weekly": "0 0 * * 0",
  "@monthly": "0 0 1 * *",
}

const two = (n: number) => String(n).padStart(2, "0")
const int = (s: string) => (/^\d+$/.test(s) ? Number(s) : NaN)

/** The "cron in words" of a schedule, or the cron itself when it isn't a common shape. */
export function describeCron(cron: string): string {
  const spec = macros[cron.trim().toLowerCase()] ?? cron.trim()
  const f = spec.split(/\s+/)
  if (f.length !== 5) return cron
  const [min, hour, dom, month, dow] = f
  if (month !== "*") return cron

  const step = /^\*\/(\d+)$/.exec(min)
  if (step && hour === "*" && dom === "*" && dow === "*") return step[1] === "1" ? "Cada minuto" : `Cada ${step[1]} minutos`
  const m = int(min)
  if (Number.isNaN(m)) return cron
  if (hour === "*" && dom === "*" && dow === "*") return m === 0 ? "Cada hora" : `Cada hora, al minuto ${m}`
  const hstep = /^\*\/(\d+)$/.exec(hour)
  if (hstep && m === 0 && dom === "*" && dow === "*") return hstep[1] === "1" ? "Cada hora" : `Cada ${hstep[1]} horas`
  const h = int(hour)
  if (Number.isNaN(h)) return cron
  const at = `a las ${two(h)}:${two(m)}`

  if (dom === "*" && dow === "*") return `Todos los días ${at}`
  if (dom === "*" && (dow === "1-5" || dow === "mon-fri")) return `De lunes a viernes ${at}`
  if (dom === "*" && (dow === "0,6" || dow === "6,0")) return `Sábados y domingos ${at}`
  if (dom === "*" && /^[0-7]$/.test(dow)) return `Los ${plural[Number(dow) % 7]} ${at}`
  if (dom === "*" && /^[0-7](,[0-7])+$/.test(dow))
    return `Los ${dow.split(",").map((d) => plural[Number(d) % 7]).join(", ")} ${at}`
  if (dow === "*" && /^\d+$/.test(dom)) return `El día ${dom} de cada mes ${at}`
  return cron
}

export const cronPresets = [
  { label: "Cada día", cron: "0 9 * * *" },
  { label: "Lunes a viernes", cron: "0 9 * * 1-5" },
  { label: "Cada lunes", cron: "0 9 * * 1" },
  { label: "Cada hora", cron: "0 * * * *" },
] as const
