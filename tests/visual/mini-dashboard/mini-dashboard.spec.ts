import { expect, test, type Page } from "@playwright/test";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

type Rect = { x: number; y: number; width: number; height: number };
type Fixture = Record<string, unknown>;

const root = dirname(fileURLToPath(import.meta.url));
const fixtureNames = [
  "healthy", "active-backup", "warning", "critical", "loomd-offline",
  "database-unavailable", "cloud-disabled", "cloud-cached-unreachable",
  "backup-never-run", "temperature-unavailable", "critical-storage",
  "stale-snapshot", "longest-labels",
] as const;
const expectedBands: Record<string, Rect> = {
  header: { x: 20, y: 16, width: 984, height: 62 },
  attention: { x: 20, y: 87, width: 984, height: 58 },
  "status-grid": { x: 20, y: 154, width: 984, height: 286 },
  network: { x: 20, y: 449, width: 984, height: 54 },
  activity: { x: 20, y: 512, width: 984, height: 72 },
};

function loadFixture(name: string): Fixture {
  const existing = join(root, "fixtures", `${name}.json`);
  if (["healthy", "warning", "critical", "loomd-offline", "unavailable"].includes(name)) {
    return JSON.parse(readFileSync(existing, "utf8"));
  }
  const fixture = JSON.parse(JSON.stringify(loadFixture("healthy"))) as any;
  const setWarning = (message: string) => {
    fixture.header.overall = "warning";
    fixture.attention = { message, severity: "warning", additional_count: 0 };
  };
  switch (name) {
    case "active-backup":
      fixture.header.overall = "active";
      fixture.attention = { message: "Backup running", severity: "active", additional_count: 0 };
      fixture.activity = { state: "ACTIVE", detail: "backup running 4m", severity: "active" };
      break;
    case "database-unavailable":
      setWarning("Database unavailable");
      fixture.runtime[1] = { key: "database", label: "Database", value: "Unavailable", severity: "warning", state: "unavailable" };
      break;
    case "cloud-disabled":
      fixture.protection[1] = { key: "cloud", label: "Cloud", value: "Disabled", severity: "unknown", state: "disabled" };
      fixture.network.detail = "1 online / 2 known - cloud disabled";
      break;
    case "cloud-cached-unreachable":
      setWarning("Cloud cache reports unreachable");
      fixture.protection[1] = { key: "cloud", label: "Cloud", value: "Failed", severity: "warning", state: "failed" };
      fixture.network = { state: "NETWORK", detail: "1 online / 2 known - cloud unreachable (cached 8m)", severity: "warning" };
      break;
    case "backup-never-run":
      setWarning("Backup has never completed");
      fixture.protection[0] = { key: "local", label: "Local", value: "Never", severity: "warning", state: "unavailable" };
      break;
    case "temperature-unavailable":
      fixture.system[1] = { key: "temperature", label: "Temp", value: "Unavailable", severity: "unknown", state: "unavailable" };
      break;
    case "critical-storage":
      fixture.header.overall = "critical";
      fixture.attention = { message: "Storage 94%", severity: "critical", additional_count: 0 };
      fixture.system[3] = { key: "storage", label: "Storage", value: "94%", severity: "critical", state: "live" };
      break;
    case "stale-snapshot":
      setWarning("Domain data stale");
      fixture.header.freshness_state = "stale";
      fixture.header.source_updated_at = "2026-08-17T13:42:08Z";
      break;
    case "longest-labels":
      setWarning("Protection requires operator review before next scheduled run");
      fixture.header.node_label = "MAIN-DISPLAY-NODE-01";
      fixture.network.detail = "2 online / 3 known - cloud unreachable (cached 11h) - communication degraded";
      fixture.activity.detail = "last activity: cloud snapshot completed 23h ago";
      fixture.attention.additional_count = 12;
      break;
  }
  return fixture;
}

async function render(page: Page, fixture: Fixture) {
  const externalRequests: string[] = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (url.hostname !== "127.0.0.1") externalRequests.push(request.url());
  });
  await page.route("**/api/status", (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(fixture) }));
  await page.goto("/", { waitUntil: "networkidle" });
  await page.waitForFunction(() => document.documentElement.dataset.ready === "true");
  await expect.poll(() => externalRequests).toEqual([]);
}

test.describe("LOOM mini-dashboard visual checkpoint", () => {
  let baselineGeometry: Record<string, Rect> | undefined;

  for (const name of fixtureNames) {
    test(`${name} renders at fixed 1024x600 geometry`, async ({ page }) => {
      await render(page, loadFixture(name));
      const expectedOverall = name === "active-backup" ? "ACTIVE" : ["critical", "loomd-offline", "critical-storage"].includes(name) ? "CRITICAL" : ["warning", "database-unavailable", "cloud-cached-unreachable", "backup-never-run", "stale-snapshot", "longest-labels"].includes(name) ? "WARNING" : "HEALTHY";
      await expect(page.locator("#overall-state")).toHaveText(expectedOverall);

      const metrics = await page.evaluate(() => ({
        viewport: { width: innerWidth, height: innerHeight },
        root: { scrollWidth: document.documentElement.scrollWidth, clientWidth: document.documentElement.clientWidth, scrollHeight: document.documentElement.scrollHeight, clientHeight: document.documentElement.clientHeight },
        body: { scrollWidth: document.body.scrollWidth, clientWidth: document.body.clientWidth, scrollHeight: document.body.scrollHeight, clientHeight: document.body.clientHeight },
        bands: Object.fromEntries(Array.from(document.querySelectorAll<HTMLElement>("[data-band]")).map((element) => {
          const rect = element.getBoundingClientRect();
          return [element.dataset.band, { x: rect.x, y: rect.y, width: rect.width, height: rect.height }];
        })),
        columns: Array.from(document.querySelectorAll<HTMLElement>("[data-column]")).map((element) => element.getBoundingClientRect().width),
        escaped: Array.from(document.querySelectorAll<HTMLElement>("body *")).filter((element) => {
          const rect = element.getBoundingClientRect();
          return rect.left < 0 || rect.top < 0 || rect.right > innerWidth || rect.bottom > innerHeight;
        }).map((element) => element.id || element.className),
        bandOverflow: Array.from(document.querySelectorAll<HTMLElement>("[data-band], [data-column]")).filter((element) => element.scrollWidth > element.clientWidth || element.scrollHeight > element.clientHeight).map((element) => element.dataset.band || element.dataset.column),
      }));

      expect(metrics.viewport).toEqual({ width: 1024, height: 600 });
      expect(metrics.root.scrollWidth).toBe(metrics.root.clientWidth);
      expect(metrics.root.scrollHeight).toBe(metrics.root.clientHeight);
      expect(metrics.body.scrollWidth).toBe(metrics.body.clientWidth);
      expect(metrics.body.scrollHeight).toBe(metrics.body.clientHeight);
      expect(metrics.escaped).toEqual([]);
      expect(metrics.bandOverflow).toEqual([]);
      expect(metrics.bands).toEqual(expectedBands);
      expect(metrics.columns).toEqual([295.1875, 295.203125, 393.609375]);

      if (!baselineGeometry) baselineGeometry = metrics.bands;
      expect(metrics.bands).toEqual(baselineGeometry);

      const fonts = await page.evaluate(async () => {
        await document.fonts.ready;
        return {
          sans: document.fonts.check('16px "Geist Sans"'),
          mono: document.fonts.check('16px "Geist Mono"'),
        };
      });
      expect(fonts).toEqual({ sans: true, mono: true });

      const screenshotPath = join(root, "screenshots", `${name}.png`);
      const screenshot = await page.screenshot({ path: screenshotPath, fullPage: false });
      expect(screenshot.byteLength).toBeGreaterThan(20_000);
    });
  }

  test("all text stays inside its owning band", async ({ page }) => {
    await render(page, loadFixture("critical"));
    const collisions = await page.evaluate(() => {
      const owners = Array.from(document.querySelectorAll<HTMLElement>("[data-band], [data-column]"));
      return owners.flatMap((owner) => {
        const bounds = owner.getBoundingClientRect();
        return Array.from(owner.querySelectorAll<HTMLElement>("h2, span, time")).filter((text) => {
          const rect = text.getBoundingClientRect();
          return rect.left < bounds.left - 0.5 || rect.right > bounds.right + 0.5 || rect.top < bounds.top - 0.5 || rect.bottom > bounds.bottom + 0.5;
        }).map((text) => text.id || text.className);
      });
    });
    expect(collisions).toEqual([]);
  });

  test("clock renders Amsterdam civil time across daylight saving changes", async ({ page }) => {
    await render(page, loadFixture("healthy"));

    await page.evaluate(() => (window as any).renderMiniDashboardClock(new Date("2026-08-29T10:49:00Z")));
    await expect(page.locator("#clock")).toHaveText("12:49");

    await page.evaluate(() => (window as any).renderMiniDashboardClock(new Date("2026-12-29T10:49:00Z")));
    await expect(page.locator("#clock")).toHaveText("11:49");
    await expect(page.locator("#clock")).toHaveAttribute("aria-label", "Amsterdam time");
  });

  test("unavailable source is explicit and keeps fixed geometry", async ({ page }) => {
    await render(page, loadFixture("unavailable"));
    await expect(page.locator("#freshness")).toHaveText("UNAVAILABLE --");
    await expect(page.locator("#freshness")).toHaveAttribute("data-state", "unavailable");
    await expect(page.locator("#freshness")).not.toContainText("LIVE");

    const metrics = await page.evaluate(() => ({
      viewport: { width: innerWidth, height: innerHeight },
      rootOverflow: document.documentElement.scrollWidth > document.documentElement.clientWidth || document.documentElement.scrollHeight > document.documentElement.clientHeight,
      bodyOverflow: document.body.scrollWidth > document.body.clientWidth || document.body.scrollHeight > document.body.clientHeight,
      bands: Object.fromEntries(Array.from(document.querySelectorAll<HTMLElement>("[data-band]")).map((element) => {
        const rect = element.getBoundingClientRect();
        return [element.dataset.band, { x: rect.x, y: rect.y, width: rect.width, height: rect.height }];
      })),
      escaped: Array.from(document.querySelectorAll<HTMLElement>("body *")).filter((element) => {
        const rect = element.getBoundingClientRect();
        return rect.left < 0 || rect.top < 0 || rect.right > innerWidth || rect.bottom > innerHeight;
      }).map((element) => element.id || element.className),
      bandOverflow: Array.from(document.querySelectorAll<HTMLElement>("[data-band], [data-column]")).filter((element) => element.scrollWidth > element.clientWidth || element.scrollHeight > element.clientHeight).map((element) => element.dataset.band || element.dataset.column),
    }));

    expect(metrics.viewport).toEqual({ width: 1024, height: 600 });
    expect(metrics.rootOverflow).toBe(false);
    expect(metrics.bodyOverflow).toBe(false);
    expect(metrics.bands).toEqual(expectedBands);
    expect(metrics.escaped).toEqual([]);
    expect(metrics.bandOverflow).toEqual([]);
  });

  test("palette contrast and minimum text size meet the checkpoint", async ({ page }) => {
    await render(page, loadFixture("critical"));
    const audit = await page.evaluate(() => {
      const style = getComputedStyle(document.documentElement);
      const colors = ["--primary", "--secondary", "--brand", "--healthy", "--warning", "--critical", "--active"];
      const surface = style.getPropertyValue("--surface").trim();
      const parse = (hex: string) => [1, 3, 5].map((offset) => Number.parseInt(hex.slice(offset, offset + 2), 16) / 255);
      const luminance = (hex: string) => parse(hex).map((channel) => channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4).reduce((sum, channel, index) => sum + channel * [0.2126, 0.7152, 0.0722][index], 0);
      const contrast = (left: string, right: string) => {
        const [lighter, darker] = [luminance(left), luminance(right)].sort((a, b) => b - a);
        return (lighter + 0.05) / (darker + 0.05);
      };
      const ratios = Object.fromEntries(colors.map((name) => [name, contrast(style.getPropertyValue(name).trim(), surface)]));
      const undersized = Array.from(document.querySelectorAll<HTMLElement>("h2, span, time")).filter((element) => {
        const rect = element.getBoundingClientRect();
        return rect.width > 0 && rect.height > 0 && Number.parseFloat(getComputedStyle(element).fontSize) < 16;
      }).map((element) => element.id || element.className);
      return { ratios, undersized };
    });
    expect(audit.undersized).toEqual([]);
    for (const [color, ratio] of Object.entries(audit.ratios)) {
      expect(ratio, `${color} contrast`).toBeGreaterThanOrEqual(4.5);
    }
  });

  test("state words remain explicit in grayscale", async ({ page }) => {
    for (const name of ["healthy", "warning", "critical", "loomd-offline"] as const) {
      await render(page, loadFixture(name));
      await page.evaluate(() => { document.body.style.filter = "grayscale(1)"; });
      const expected = name === "healthy" ? "HEALTHY" : name === "warning" ? "WARNING" : "CRITICAL";
      await expect(page.locator("#overall-state")).toHaveText(expected);
      if (name !== "healthy") await expect(page.locator("#attention-message")).toContainText(name === "loomd-offline" ? "OFFLINE" : expected);
      const screenshot = await page.screenshot({ path: join(root, "test-results", `grayscale-${name}.png`) });
      expect(screenshot.byteLength).toBeGreaterThan(20_000);
    }
  });

  test("poll failure preserves the last rendered snapshot", async ({ page }) => {
    const fixture = loadFixture("healthy");
    let available = true;
    await page.route("**/api/status", (route) => available ? route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(fixture) }) : route.abort());
    await page.goto("/");
    await page.waitForFunction(() => document.documentElement.dataset.ready === "true");
    await expect(page.locator("#overall-state")).toHaveText("HEALTHY");
    available = false;
    await page.evaluate(() => (window as any).pollMiniDashboardStatus());
    await page.waitForFunction(() => document.documentElement.dataset.connection === "offline");
    await expect(page.locator("#overall-state")).toHaveText("HEALTHY");
  });
});
