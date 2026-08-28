import { describe, expect, it, vi } from "vitest";
import { UnauthorizedError } from "./api";
import { readAuthBootstrap, recoverExpiredSession } from "./auth";

describe("browser authentication bootstrap", () => {
  it("reads trusted identity metadata", () => {
    const documentRoot = document.implementation.createHTMLDocument();
    documentRoot.head.innerHTML = '<meta name="tissues-auth-enabled" content="true"><meta name="tissues-author" content="person@example.test">';
    expect(readAuthBootstrap(documentRoot)).toEqual({ enabled: true, author: "person@example.test" });
  });

  it("navigates the full page to the exact current URL only for API 401", () => {
    const navigate = vi.fn();
    const currentURL = "https://tissues.example.test/?issue=abc&view=closed";
    expect(recoverExpiredSession(new UnauthorizedError("expired"), currentURL, navigate)).toBe(true);
    expect(navigate).toHaveBeenCalledWith(currentURL);
    navigate.mockClear();
    expect(recoverExpiredSession(new Error("offline"), currentURL, navigate)).toBe(false);
    expect(navigate).not.toHaveBeenCalled();
  });
});
