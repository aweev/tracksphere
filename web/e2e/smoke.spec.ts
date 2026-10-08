import { expect, test } from '@playwright/test';

// Smoke: the built web tier serves auth + public portal shells with no
// backend dependency (both render client-side; API failures surface inline).
// Full product behavior stays in scripts/e2e.ps1 against the live stack.
test('login renders', async ({ page }) => {
  await page.goto('/login');
  await expect(page.getByRole('button', { name: /sign in/i })).toBeVisible();
});

test('public track lookup renders', async ({ page }) => {
  await page.goto('/track');
  await expect(page.getByPlaceholder(/tracking number/i)).toBeVisible();
});

test('status page renders', async ({ page }) => {
  await page.goto('/status');
  await expect(page.getByText(/system status/i)).toBeVisible();
});

test('embed widget renders chromeless tracker', async ({ page }) => {
  await page.goto('/embed/TS-8842-LAG');
  await expect(page.getByRole('search')).toBeVisible();
});
