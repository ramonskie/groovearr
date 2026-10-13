import { describe, it, expect } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import SidebarNav from "./SidebarNav";

// This repo's vitest runs in a plain node environment (no jsdom), so components
// are asserted with react-dom/server static rendering — SidebarNav uses no
// router hooks (navigation is a callback), so no MemoryRouter is needed.
describe("SidebarNav version", () => {
  it("renders the version above the Settings tab for an admin", () => {
    const html = renderToStaticMarkup(
      <SidebarNav
        activePage="settings"
        onNavigate={() => {}}
        isAdmin
        version="v0.9.0"
      />,
    );
    expect(html).toContain("v0.9.0");
    // "above settings" → the version text precedes the Settings label.
    const versionAt = html.indexOf("v0.9.0");
    const settingsAt = html.indexOf("Settings");
    expect(versionAt).toBeGreaterThanOrEqual(0);
    expect(settingsAt).toBeGreaterThanOrEqual(0);
    expect(versionAt).toBeLessThan(settingsAt);
  });

  it("omits the version row when the version is unknown", () => {
    const html = renderToStaticMarkup(
      <SidebarNav activePage="discover" onNavigate={() => {}} isAdmin />,
    );
    expect(html).not.toContain("v0.9.0");
    // The row itself (its title attribute) must be absent, not just the value.
    expect(html).not.toContain("title=\"Groovearr");
  });
});
