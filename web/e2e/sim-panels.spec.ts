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

test('playwright spec: sim panels', async ({ page }) => {
  const bodies = await mockApi(page, () => []);

  // 1. Load Loan Underwriting
  await page.goto('/');
  await page.locator('header select').selectOption({ label: 'Loan Underwriting (Simulation)' });

  // 2. Set recession.p_bad to 0.4 and blur
  const pBadInput = page.getByTestId('world-cell-recession-p_bad');
  await expect(pBadInput).toHaveValue('0.35');
  await pBadInput.fill('0.4');
  await pBadInput.blur();

  const simEditor = page.getByTestId('sim-editor-sim');
  const text = await simEditor.inputValue();
  expect(text).toContain('p_bad: 0.4');

  // check that base still has p_bad: 0.2
  // We can't strictly assert this string directly, but we can check if it parses correctly.
  expect(text).toContain('p_bad: 0.2');

  // 3. Click Validate
  await page.getByTestId('sim-validate').click();
  await expect(page.getByTestId('sim-validate-ok')).toHaveText('No errors');
  expect(bodies).toHaveLength(1);
  expect(bodies[0].sim).toContain('0.4');

  // 4. Add the world boom
  await page.getByPlaceholder('New world name').fill('boom');
  await page.getByTestId('world-add').click();
  const simTextAfterBoom = await simEditor.inputValue();
  expect(simTextAfterBoom).toContain('boom:');
  await expect(page.getByTestId('world-cell-boom-p_bad')).toBeVisible();

  // 5. Change the objective name to profit2 and Apply
  await page.getByTestId('sim-objective').locator('input').first().fill('profit2');
  await page.getByTestId('objective-apply').click();
  const simTextAfterObjective = await simEditor.inputValue();
  expect(simTextAfterObjective).toContain('name: profit2');
});
