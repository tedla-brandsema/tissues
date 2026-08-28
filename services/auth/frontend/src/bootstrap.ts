export interface LoginBootstrap {
  action: string;
  next: string;
  nextExp: string;
  nextSig: string;
  invalidCredentials: boolean;
}

export function readLoginBootstrap(location: Pick<Location, "pathname" | "search"> = window.location): LoginBootstrap {
  const query = new URLSearchParams(location.search);
  return {
    action: location.pathname.replace(/\/+$/, "") || "/",
    next: query.get("next") || "",
    nextExp: query.get("next_exp") || "",
    nextSig: query.get("next_sig") || "",
    invalidCredentials: query.get("error") === "invalid_credentials",
  };
}
