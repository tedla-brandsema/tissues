import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { App } from "./App";
import type { LoginBootstrap } from "./bootstrap";

const bootstrap: LoginBootstrap = {
  action: "/auth/login",
  next: "/authorize?client_id=tissues",
  nextExp: "1234567890",
  nextSig: "signed-return",
  invalidCredentials: false,
};

describe("auth login", () => {
  it("renders an accessible native login form with exact-return fields", () => {
    const { container } = render(<App bootstrap={bootstrap} />);

    expect(screen.getByRole("heading", { name: "Sign in" })).toBeInTheDocument();
    expect(screen.getByLabelText("Email")).toHaveAttribute("autocomplete", "username");
    expect(screen.getByLabelText("Password")).toHaveAttribute("autocomplete", "current-password");
    expect(container.querySelector("form")).toHaveAttribute("action", "/auth/login");
    expect(container.querySelector('input[name="next"]')).toHaveValue(bootstrap.next);
    expect(container.querySelector('input[name="next_exp"]')).toHaveValue(bootstrap.nextExp);
    expect(container.querySelector('input[name="next_sig"]')).toHaveValue(bootstrap.nextSig);
  });

  it("enters a disabled loading state without persisting credentials", async () => {
    const storage = vi.spyOn(Storage.prototype, "setItem");
    const { container } = render(<App bootstrap={bootstrap} />);
    await userEvent.type(screen.getByLabelText("Email"), "person@example.test");
    await userEvent.type(screen.getByLabelText("Password"), "not-a-real-password");

    fireEvent.submit(container.querySelector("form")!);

    expect(screen.getByRole("button", { name: "Signing in…" })).toBeDisabled();
    expect(storage).not.toHaveBeenCalled();
    storage.mockRestore();
  });

  it("shows only the generic invalid-credentials message", () => {
    render(<App bootstrap={{ ...bootstrap, invalidCredentials: true }} />);
    expect(screen.getByRole("alert")).toHaveTextContent("Invalid email or password.");
  });
});
