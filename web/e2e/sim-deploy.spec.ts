import { test, expect } from '@playwright/test';

test('Deploy mode: diff and shadow deploy the loan policy', async ({ page }) => {
  // Mock the deploy endpoint
  let deployCalled = false;
  let deployMode = '';
  await page.route('**/v1/policies/*:deploy', async (route) => {
    deployCalled = true;
    const request = route.request();
    const body = request.postDataJSON();
    deployMode = body.mode;

    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        id: 'loan-underwriting',
        version: 'recession-v1.yaml',
        mode: body.mode,
      }),
    });
  });

  // Mock the drift endpoint
  await page.route('**/v1/models/*/drift*', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        report: {
          policyVersion: 'recession-v1.yaml',
          driftDetected: true,
          nodes: {
            'route': {
              observedSamples: 1500,
              driftDetected: true,
              failureRateDrift: { predicted: 0.05, observed: 0.1, delta: 0.05, isDrift: true },
            },
          },
        },
      }),
    });
  });

  await page.goto('/');

  // Load the loan example
  await page.getByRole('combobox').selectOption({ label: 'Loan Underwriting (Simulation)' });

  // Navigate to Deploy tab
  await page.click('[data-testid="sim-tab-deploy"]');

  // Verify deploy panel elements exist
  await expect(page.getByRole('heading', { name: 'Deploy Mode' })).toBeVisible();

  // Verify policy diff table exists and reports divergence
  await expect(page.locator('[data-testid="policy-diff-table"]')).toBeVisible();
  await expect(page.locator('[data-testid="policy-diff-diff-count"]')).toBeVisible();

  // Set policy version and select shadow mode
  await page.fill('[data-testid="deploy-policy-input"]', 'recession-v1.yaml');
  await page.selectOption('[data-testid="deploy-mode-select"]', 'shadow');

  // Trigger deploy
  await page.click('[data-testid="deploy-button"]');

  // Wait for success status
  await expect(page.locator('[data-testid="deploy-status"]')).toContainText('Successfully deployed policy in shadow mode.');

  expect(deployCalled).toBe(true);
  expect(deployMode).toBe('shadow');

  // Verify counterfactual choices on executions
  await expect(page.locator('[data-testid="counterfactual-executions"]')).toBeVisible();
  await expect(page.locator('[data-testid="counterfactual-choice-route"]')).toContainText('review');

  // Load Drift Report
  await page.click('[data-testid="load-drift-button"]');

  // Wait for drift status
  await expect(page.locator('[data-testid="deploy-status"]')).toContainText('Drift report loaded.');

  // Verify drift indicator on canvas node 'route'
  await expect(page.locator('[data-testid="sim-node-drift-route"]')).toBeVisible();
});
