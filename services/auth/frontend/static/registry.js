const COMPONENT_MAP = {
  "tissues-auth-login": () => import("tissues-auth-login"),
};

export async function bootstrapGCPAuthUI() {
  const tags = Object.keys(COMPONENT_MAP);
  const discovered = tags.filter((tag) => document.querySelector(tag));
  await Promise.all(discovered.map((tag) => COMPONENT_MAP[tag]()));
}
