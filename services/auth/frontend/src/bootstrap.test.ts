import { describe, expect, it } from "vitest";
import { readLoginBootstrap } from "./bootstrap";

describe("login bootstrap", () => {
  it("uses the current same-origin path and preserves signed return values", () => {
    expect(readLoginBootstrap({
      pathname: "/auth/login/",
      search: "?next=%2Fauthorize%3Fclient_id%3Dtissues&next_exp=123&next_sig=signed&error=invalid_credentials",
    } as Location)).toEqual({
      action: "/auth/login",
      next: "/authorize?client_id=tissues",
      nextExp: "123",
      nextSig: "signed",
      invalidCredentials: true,
    });
  });
});
