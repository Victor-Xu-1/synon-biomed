import { expect, test } from "@playwright/test";
import { loginToScientificWorkbench } from "./synonBiomedScientificFixture";

test.describe("Synon Biomed personal account dashboard", () => {
  test("renders real account activity and preserves profile controls across responsive layouts", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width: 1440, height: 1400 });
    await loginToScientificWorkbench(page);
    const pageErrors: string[] = [];
    page.on("pageerror", (error) => pageErrors.push(error.message));
    const overviewResponse = page.waitForResponse((response) =>
      response.url().includes("/api/account/overview"),
    );
    await page.goto("/#/settings/account", { waitUntil: "domcontentloaded" });
    const overview = await overviewResponse;
    expect(overview.status()).toBe(200);

    await expect(page.getByRole("heading", { name: "个人账户" })).toBeVisible();
    await expect(page.getByRole("heading", { name: "工作活动" })).toBeVisible();
    await expect(page.getByRole("heading", { name: "活动洞察" })).toBeVisible();
    await expect(page.getByRole("heading", { name: "最常用的 Skills" })).toBeVisible();
    await expect(page.locator(".account-metric")).toHaveCount(5);
    await expect(page.locator(".account-profile-network")).toBeVisible();
    await expect(
      page.locator('.account-activity-chart[data-mode="daily"] .account-activity-chart__point'),
    ).toHaveCount(30);

    await page.getByRole("tab", { name: "每周" }).click();
    await expect(page.getByRole("tab", { name: "每周" })).toHaveAttribute("aria-selected", "true");
    await expect(
      page.locator('.account-activity-chart[data-mode="weekly"] .account-activity-chart__point'),
    ).toHaveCount(26);
    await page.getByRole("tab", { name: "每日" }).click();

    await page.getByRole("button", { name: "隐私说明" }).click();
    await expect(page.getByRole("dialog")).toContainText("资料仅保存在当前浏览器");
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog")).toBeHidden();

    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= document.documentElement.clientWidth,
      ),
    ).toBe(true);
    const desktopScreenshot = testInfo.outputPath("account-profile-desktop.png");
    await page.screenshot({ path: desktopScreenshot, fullPage: true });
    await testInfo.attach("account-profile-desktop", {
      path: desktopScreenshot,
      contentType: "image/png",
    });

    await page.setViewportSize({ width: 760, height: 1100 });
    await expect(page.locator(".account-metrics")).toBeVisible();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= document.documentElement.clientWidth,
      ),
    ).toBe(true);
    const compactScreenshot = testInfo.outputPath("account-profile-compact.png");
    await page.screenshot({ path: compactScreenshot, fullPage: true });
    await testInfo.attach("account-profile-compact", {
      path: compactScreenshot,
      contentType: "image/png",
    });

    expect(pageErrors).toEqual([]);
  });
});
