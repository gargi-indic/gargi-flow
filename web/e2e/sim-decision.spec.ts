import { test, expect, type Page } from '@playwright/test';

const API = 'http://api.gargi.test';

type Issue = { path: string; code: string; message: string };

// Mocks the API and records the body of each validate request.
async function mockApi(page: Page, respond: () => Issue[]) {
  const bodies: { sim?: string; graph?: string; format?: string }[] = [];
  await page.route(`${API}/**`, async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === '/v1/models:validate') {
      bodies.push(route.request().postDataJSON());
      const errors = respond();
      return route.fulfill({ json: { valid: errors.length === 0, errors } });
    }
    if (url.pathname === '/v1/executions') {
      return route.fulfill({ json: { executions: [] } });
    }
    return route.fulfill({ status: 404, json: { message: 'not mocked' } });
  });
  return bodies;
}

test('playwright spec exact error matching', async ({ page }) => {
  await mockApi(page, () => [
    {
      path: "$.graph:$.nodes[2].config.observes[2]",
      code: "observes_non_ancestor",
      message: "observed node \"human_review\" is not a strict ancestor of \"route\"",
    },
  ]);

  // Load Loan Underwriting
  await page.goto('/');
  await page.locator('header select').selectOption({ label: 'Loan Underwriting (Simulation)' });

  // Wait for diamonds to appear
  await expect(page.getByTestId('sim-node-screen')).toBeVisible();
  await expect(page.getByTestId('sim-node-route')).toBeVisible();
  await expect(page.getByTestId('sim-node-final')).toBeVisible();

  // Switch to graph tab first
  await page.getByTestId('sim-tab-graph').click();

  // Click route and add human_review.output.result (force true to bypass textarea overlap)
  await page.getByTestId('sim-node-route').click({ force: true });
  const observesField = page.getByTestId('decision-observes');
  const currentObserves = await observesField.inputValue();
  await observesField.fill(currentObserves + '\nhuman_review.output.result');
  await page.getByTestId('decision-apply').click();

  // Verify graph.yaml text updates
  const textarea = page.getByTestId('sim-editor-graph');
  await expect(textarea).toHaveValue(/human_review\.output\.result/);

  // Click validate
  await page.getByTestId('sim-validate').click();

  // Verify node error
  const badge = page.getByTestId('sim-node-error-route');
  await expect(badge).toBeVisible();
  const title = await badge.getAttribute('title');
  expect(title).toContain('not a strict ancestor');

  // Add decision gate
  await page.getByPlaceholder('Decision ID').fill('gate');
  await page.getByTestId('sim-add-decision').click();

  // Select gate (force true to bypass textarea overlap)
  await page.getByTestId('sim-node-gate').click({ force: true });

  // Fill options
  await page.getByTestId('decision-options').fill('a,b');
  await page.getByTestId('decision-apply').click();

  // Verify textarea changes
  await expect(textarea).toHaveValue(/id:\s*gate/);
  await expect(textarea).toHaveValue(/type:\s*decision/);

  // Ensure returning to canvas doesn't leak
  await page.getByTestId('sim-toggle').click();
  await expect(page.getByTestId('sim-node-gate')).toBeHidden();
  await expect(page.getByTestId('sim-node-screen')).toBeHidden();
});
