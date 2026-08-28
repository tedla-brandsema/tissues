import { UnauthorizedError } from "./api";

export type AuthBootstrap = { enabled: boolean; author: string };

export function readAuthBootstrap(documentRoot: Document = document): AuthBootstrap {
  const value = (name: string) => documentRoot.querySelector<HTMLMetaElement>(`meta[name="${name}"]`)?.content || "";
  return { enabled: value("tissues-auth-enabled") === "true", author: value("tissues-author") };
}

export function recoverExpiredSession(
  cause: unknown,
  currentURL: string = window.location.href,
  navigate: (url: string) => void = (url) => window.location.assign(url),
): boolean {
  if (!(cause instanceof UnauthorizedError)) return false;
  navigate(currentURL);
  return true;
}
