import * as React from "react"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it, vi } from "vitest"

import { ChipInput } from "./chip-input"

function Harness({ initial = [] as string[], onChange = vi.fn() }) {
  const [value, setValue] = React.useState(initial)
  return (
    <ChipInput
      aria-label="Hosts"
      value={value}
      onChange={(v) => {
        setValue(v)
        onChange(v)
      }}
      placeholder="registry.npmjs.org"
    />
  )
}

describe("ChipInput", () => {
  it("adds on Enter, comma and space, dropping duplicates", async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<Harness onChange={onChange} />)
    const input = screen.getByRole("textbox", { name: "Hosts" })
    await user.type(input, "nodejs{Enter}npm,nodejs ")
    expect(onChange).toHaveBeenLastCalledWith(["nodejs", "npm"])
    expect(screen.getByText("nodejs")).toBeInTheDocument()
    expect(screen.getByText("npm")).toBeInTheDocument()
    expect(input).toHaveValue("")
  })

  it("commits what is typed on blur and splits a pasted list", async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<Harness onChange={onChange} />)
    const input = screen.getByRole("textbox", { name: "Hosts" })
    await user.type(input, "make")
    await user.tab()
    expect(onChange).toHaveBeenLastCalledWith(["make"])
    await user.click(input)
    await user.paste("pypi.org, files.pythonhosted.org\nproxy.golang.org")
    expect(onChange).toHaveBeenLastCalledWith(["make", "pypi.org", "files.pythonhosted.org", "proxy.golang.org"])
  })

  it("removes with × and with Backspace on an empty field", async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<Harness initial={["a", "b", "c"]} onChange={onChange} />)
    await user.click(screen.getByRole("button", { name: "Quitar b" }))
    expect(onChange).toHaveBeenLastCalledWith(["a", "c"])
    await user.click(screen.getByRole("textbox", { name: "Hosts" }))
    await user.keyboard("{Backspace}")
    expect(onChange).toHaveBeenLastCalledWith(["a"])
  })

  it("read-only shows the chips with no input and no remove buttons", () => {
    render(<ChipInput aria-label="Hosts" value={["a"]} onChange={vi.fn()} readOnly />)
    expect(screen.getByText("a")).toBeInTheDocument()
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument()
    expect(screen.queryByRole("button")).not.toBeInTheDocument()
  })
})
