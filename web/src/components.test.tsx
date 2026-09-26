import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { FormField } from "./components";
describe("form accessibility names", () => {
  it("keeps password hints out of the exact field name while exposing them as descriptions", () => {
    render(
      <FormField label="密码" hint="至少 12 个字符。">
        <input type="password" autoComplete="new-password" />
      </FormField>,
    );
    const input = screen.getByLabelText("密码", { exact: true });
    expect(input.tagName).toBe("INPUT");
    const hint = document.getElementById(
      input.getAttribute("aria-describedby")!,
    );
    expect(hint?.textContent).toBe("至少 12 个字符。");
    expect((document.querySelector("label") as HTMLLabelElement).htmlFor).toBe(
      input.id,
    );
  });
});
