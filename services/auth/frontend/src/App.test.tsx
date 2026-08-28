import { fireEvent, render, screen } from "@testing-library/react";
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

  it("keeps credentials and exact-return fields successful during native submission", () => {
    const storage = vi.spyOn(Storage.prototype, "setItem");
    const { container } = render(<App bootstrap={bootstrap} />);
    const email = container.querySelector<HTMLInputElement>("#email")!;
    const password = container.querySelector<HTMLInputElement>("#password")!;
    const form = container.querySelector("form")!;
    let submitted: FormData | undefined;
    document.addEventListener("submit", (event) => {
      event.preventDefault();
      submitted = new window.FormData(form);
    }, { once: true });
    email.value = "person@example.test";
    password.value = "not-a-real-password";

    fireEvent.submit(form);

    expect(email).not.toBeDisabled();
    expect(password).not.toBeDisabled();
    expect(Object.fromEntries(submitted!)).toEqual({
      email: "person@example.test",
      password: "not-a-real-password",
      next: bootstrap.next,
      next_exp: bootstrap.nextExp,
      next_sig: bootstrap.nextSig,
    });
    expect(storage).not.toHaveBeenCalled();
    storage.mockRestore();
  });

  it("shows only the generic invalid-credentials message", () => {
    render(<App bootstrap={{ ...bootstrap, invalidCredentials: true }} />);
    expect(screen.getByRole("alert")).toHaveTextContent("Invalid email or password.");
  });
});
