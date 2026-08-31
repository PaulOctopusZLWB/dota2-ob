import { expect, test } from "@playwright/test";
import { readFileSync } from "node:fs";

const families = [
  "draft-context",
  "lane-checkpoint",
  "item-timing-long",
  "objective-exchange",
  "teamfight-readiness"
];

const canvases = [
  { name: "1080p", width: 1920, height: 1080 },
  { name: "1440p", width: 2560, height: 1440 }
];

const fixtureBodies = new Map(
  ["healthy", "stale", "emergency-hide"].map((name) => [
    name,
    readFileSync(new URL(`../fixtures/${name}.json`, import.meta.url), "utf8")
  ])
);

async function openFixture(page, fixture) {
  await page.goto(`/?fixture=${fixture}`);
  await expect(page.locator("body")).toHaveAttribute("data-render-state", "visible");
}

async function expectNoOverflow(page) {
  const result = await page.locator("#overlay-card").evaluate((card) => {
    const viewport = { width: window.innerWidth, height: window.innerHeight };
    const rect = card.getBoundingClientRect();
    const header = card.querySelector(".card-header").getBoundingClientRect();
    const claim = card.querySelector(".claim").getBoundingClientRect();
    const footer = card.querySelector(".card-footer").getBoundingClientRect();
    const clippedText = [...card.querySelectorAll("h1, p, .provenance span, .provenance strong")]
      .filter((element) => {
        const style = getComputedStyle(element);
        const clipsX = ["hidden", "clip"].includes(style.overflowX) && element.scrollWidth > element.clientWidth + 1;
        const clipsY = ["hidden", "clip"].includes(style.overflowY) && element.scrollHeight > element.clientHeight + 1;
        return clipsX || clipsY;
      })
      .map((element) => element.id || element.className || element.tagName);
    return {
      insideViewport: rect.left >= 0 && rect.top >= 0 && rect.right <= viewport.width && rect.bottom <= viewport.height,
      cardOverflow: card.scrollWidth > card.clientWidth + 1 || card.scrollHeight > card.clientHeight + 1,
      sectionsOrdered: header.bottom <= claim.top && claim.bottom <= footer.top,
      clippedText
    };
  });
  expect(result).toEqual({ insideViewport: true, cardOverflow: false, sectionsOrdered: true, clippedText: [] });
}

async function expectNewerUnsafeResponseToWin(page, unsafeOutcome) {
  const unsafeBody = unsafeOutcome === "malformed"
    ? JSON.stringify({ schemaVersion: "overlay-state/v1", rawGsi: { player: "forbidden" } })
    : fixtureBodies.get(unsafeOutcome);
  await page.addInitScript(({ healthyBody, unsafeBody, unsafeOutcome }) => {
    const response = (body) => new Response(body, {
      status: 200,
      headers: { "Content-Type": "application/json" }
    });
    let releaseDelayedHealthy;
    window.__overlayPollCount = 0;
    window.__releaseDelayedHealthy = () => releaseDelayedHealthy();
    window.fetch = async () => {
      window.__overlayPollCount += 1;
      if (window.__overlayPollCount === 1) return response(healthyBody);
      if (window.__overlayPollCount === 2) {
        return new Promise((resolve) => {
          releaseDelayedHealthy = () => resolve(response(healthyBody));
        });
      }
      if (window.__overlayPollCount === 3) {
        if (unsafeOutcome === "disconnected") throw new TypeError("connection failed");
        return response(unsafeBody);
      }
      // Keep subsequent polls pending so only the deliberately ordered
      // responses below can affect the assertion.
      return new Promise(() => {});
    };
  }, { healthyBody: fixtureBodies.get("healthy"), unsafeBody, unsafeOutcome });

  await openFixture(page, "healthy");
  await expect.poll(() => page.evaluate(() => window.__overlayPollCount)).toBeGreaterThanOrEqual(3);
  await expect(page.locator("body")).toHaveAttribute("data-render-state", "hidden", { timeout: 2_000 });

  await page.evaluate(() => {
    window.__visibleAfterUnsafe = false;
    const observer = new MutationObserver(() => {
      if (document.body.dataset.renderState === "visible") window.__visibleAfterUnsafe = true;
    });
    observer.observe(document.body, { attributes: true, attributeFilter: ["data-render-state"] });
    window.__releaseDelayedHealthy();
  });
  await page.waitForTimeout(350);
  expect(await page.evaluate(() => window.__visibleAfterUnsafe)).toBe(false);
  expect(await page.locator("body").getAttribute("data-render-state")).toBe("hidden");
  expect(await page.locator("#claim").isHidden()).toBe(true);
}

test("uses a transparent canvas and localhost resources only", async ({ page }) => {
  const remoteRequests = [];
  page.on("request", (request) => {
    if (new URL(request.url()).hostname !== "127.0.0.1") remoteRequests.push(request.url());
  });

  await openFixture(page, "healthy");

  const backgrounds = await page.evaluate(() => ({
    html: getComputedStyle(document.documentElement).backgroundColor,
    body: getComputedStyle(document.body).backgroundColor
  }));
  expect(backgrounds).toEqual({ html: "rgba(0, 0, 0, 0)", body: "rgba(0, 0, 0, 0)" });
  expect(remoteRequests).toEqual([]);
  await expect(page.locator("html")).toHaveAttribute("lang", "zh-CN");
});

for (const canvas of canvases) {
  for (const fixture of families) {
    test(`${fixture} has no clipping or overlap at ${canvas.name}`, async ({ page }) => {
      await page.setViewportSize(canvas);
      await openFixture(page, fixture);
      await expectNoOverflow(page);
    });
  }
}

for (const canvas of canvases) {
  test(`longest Chinese fixture matches the ${canvas.name} visual baseline`, async ({ page }) => {
    await page.setViewportSize(canvas);
    await openFixture(page, "item-timing-long");
    await expect(page).toHaveScreenshot(`overlay-${canvas.name}.png`, {
      animations: "disabled",
      caret: "hide",
      omitBackground: true
    });
  });
}

test("a missing local asset keeps stable geometry and shows its text fallback", async ({ page }) => {
  let releaseAsset;
  await page.route("**/assets/missing-hero.webp", async (route) => {
    await new Promise((resolve) => { releaseAsset = resolve; });
    await route.fulfill({ status: 404, body: "missing" });
  });
  await page.goto("/?fixture=missing-asset", { waitUntil: "domcontentloaded" });
  await expect(page.locator("body")).toHaveAttribute("data-render-state", "visible");
  await expect.poll(() => Boolean(releaseAsset)).toBe(true);
  await page.locator("#overlay-card").evaluate((card) => card.getAnimations().map((animation) => animation.finish()));
  const before = await page.locator("#overlay-card").boundingBox();
  await expect(page.locator("#asset-fallback")).toBeVisible();
  releaseAsset();
  await page.locator("#asset-image").evaluate((image) => new Promise((resolve) => {
    if (image.complete) resolve();
    else image.addEventListener("error", resolve, { once: true });
  }));
  const after = await page.locator("#overlay-card").boundingBox();
  expect(after).toEqual(before);
  await expectNoOverflow(page);
});

for (const fixture of ["stale", "disconnected", "emergency-hide"]) {
  test(`${fixture} hides every analytical claim promptly`, async ({ page }) => {
    const startedAt = Date.now();
    await page.goto(`/?fixture=${fixture}`);
    await expect(page.locator("body")).toHaveAttribute("data-render-state", "hidden");
    expect(Date.now() - startedAt).toBeLessThan(2_000);
    await expect(page.locator("#claim")).toBeHidden();
  });
}

test("malformed or contract-unsafe state fails closed", async ({ page }) => {
  await page.route("**/fixtures/healthy.json", (route) => route.fulfill({
    contentType: "application/json",
    body: JSON.stringify({ schemaVersion: "overlay-state/v1", rawGsi: { player: "forbidden" } })
  }));
  await page.goto("/?fixture=healthy");
  await expect(page.locator("body")).toHaveAttribute("data-render-state", "hidden");
  await expect(page.locator("#claim")).toBeHidden();
});

test("connection loss hides within two seconds and a valid response reconnects", async ({ page }) => {
  let connected = true;
  await page.route("**/fixtures/healthy.json", (route) => connected ? route.continue() : route.abort("connectionfailed"));
  await openFixture(page, "healthy");

  connected = false;
  const disconnectedAt = Date.now();
  await expect(page.locator("body")).toHaveAttribute("data-render-state", "hidden", { timeout: 2_000 });
  const hiddenAfterMs = Date.now() - disconnectedAt;
  expect(hiddenAfterMs).toBeLessThan(2_000);

  connected = true;
  await expect(page.locator("body")).toHaveAttribute("data-render-state", "visible", { timeout: 2_000 });
  await expect(page.locator("#claim")).toBeVisible();
});

for (const unsafeOutcome of ["stale", "malformed", "disconnected", "emergency-hide"]) {
  test(`an older healthy response cannot revive claims after newer ${unsafeOutcome}`, async ({ page }) => {
    await expectNewerUnsafeResponseToWin(page, unsafeOutcome);
  });
}

test("a healthy response slower than the poll interval still renders", async ({ page }) => {
  await page.addInitScript((healthyBody) => {
    window.fetch = async () => {
      await new Promise((resolve) => setTimeout(resolve, 400));
      return new Response(healthyBody, {
        status: 200,
        headers: { "Content-Type": "application/json" }
      });
    };
  }, fixtureBodies.get("healthy"));

  await page.goto("/?fixture=healthy");
  await expect(page.locator("body")).toHaveAttribute("data-render-state", "visible", { timeout: 2_000 });
});
