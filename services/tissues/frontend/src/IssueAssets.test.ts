import { describe, expect, it } from "vitest";
import { assetPresentationURL, formatAssetSize } from "./IssueAssets";

const asset = {
  name: "browser_crash.jpg",
  url: "/api/tissues/v1/issues/FLUENT-1/assets/browser_crash.jpg",
  content_type: "image/jpeg" as const,
  width: 1200,
  height: 800,
  size: 483221,
};

describe("Issue image presentation", () => {
  it("formats processed sizes", () => {
    expect(formatAssetSize(asset.size)).toBe("472 KB");
    expect(formatAssetSize(2 * 1024 * 1024)).toBe("2.0 MB");
  });

  it("revises only the browser presentation URL", () => {
    expect(assetPresentationURL(asset)).toBe(asset.url);
    expect(assetPresentationURL(asset, 1)).toBe(`${asset.url}?preview=1`);
    expect(assetPresentationURL(asset, 2)).toBe(`${asset.url}?preview=2`);
  });
});
