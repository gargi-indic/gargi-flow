import { test, expect } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';
import * as yaml from 'js-yaml';
import { fileURLToPath } from 'url';
import { dirname } from 'path';

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);

test('Simulate panel runs experiment and shows results', async ({ page }) => {
  // Mock POST /v1/models
  await page.route('**/v1/models', async (route) => {
    if (route.request().method() === 'POST') {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          model: { id: 'loan-underwriting', version: '1' }
        })
      });
    } else {
      await route.fallback();
    }
  });

  // Mock POST /v1/experiments
  let experimentReqPromiseResolve: (value: Record<string, unknown>) => void;
  const experimentReqPromise = new Promise<Record<string, unknown>>((resolve) => {
    experimentReqPromiseResolve = resolve;
  });
  let experimentReqBody: Record<string, unknown> | null = null;
  await page.route('**/v1/experiments', async (route) => {
    if (route.request().method() === 'POST') {
      experimentReqBody = route.request().postDataJSON() as Record<string, unknown>;
      experimentReqPromiseResolve(experimentReqBody);
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          id: 'exp-1',
          status: 'EXPERIMENT_STATUS_QUEUED'
        })
      });
    } else {
      await route.fallback();
    }
  });

  // Mock GET events
  await page.route('**/v1/experiments/exp-1/events', async (route) => {
    const sseBody = `id: 1\nevent: progress\ndata: {"id":"exp-1","sequence":"1","eventType":"progress","status":"EXPERIMENT_STATUS_RUNNING","progress":{"done":"42","total":"84","best":"loan-underwriting@exhaustive-b1b8bd20353bca7f","bestMean":460}}\n\nid: 2\nevent: done\ndata: {"id":"exp-1","sequence":"2","eventType":"done","status":"EXPERIMENT_STATUS_SUCCEEDED","progress":{"done":"84","total":"84","best":"loan-underwriting@exhaustive-b1b8bd20353bca7f","bestMean":460}}\n\n`;
    await route.fulfill({
      status: 200,
      contentType: 'text/event-stream',
      body: sseBody,
    });
  });

  // Mock GET reports/summary
  await page.route('**/v1/experiments/exp-1/reports/summary', async (route) => {
    const summaryData = JSON.parse(fs.readFileSync(path.join(__dirname, 'fixtures', 'loan-report.json'), 'utf8'));
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(summaryData),
    });
  });

  // Load the page
  await page.goto('/');

  // Select the "Loan Underwriting (Simulation)" option
  await page.getByRole('combobox').selectOption({ label: 'Loan Underwriting (Simulation)' });

  // Wait for the sim model to load by checking for the Simulate tab
  await expect(page.getByTestId('sim-tab-experiment')).toBeVisible();

  // Go to Simulate mode tab
  await page.getByTestId('sim-tab-experiment').click();

  // Verify elements are visible
  await expect(page.getByTestId('sim-simulate')).toBeVisible();

  // Ensure worlds are loaded and checked
  const worldBase = page.locator('input[data-testid="simulate-world-base"]');
  const worldRecession = page.locator('input[data-testid="simulate-world-recession"]');

  await expect(worldBase).toBeVisible();
  await expect(worldRecession).toBeVisible();

  await expect(worldBase).toBeChecked();
  await expect(worldRecession).toBeChecked();

  await expect(page.getByTestId('simulate-run')).toBeEnabled();

  // Run the experiment
  await page.getByTestId('simulate-run').click();

  // Wait for the requests to be intercepted
  await experimentReqPromise;

  // Assert POST body for experiment
  expect(experimentReqBody).not.toBeNull();
  if (experimentReqBody) {
    const experiment = yaml.load(experimentReqBody.experiment as string) as Record<string, unknown>;
    expect((experiment.policies as Record<string, unknown>).search).toBe('exact');
    expect(experiment.evaluator).toBe('exact');
    expect((experiment.worlds as string[])).toContain('base');
    expect((experiment.worlds as string[])).toContain('recession');
  }

  // Expect progress UI to show up during SSE
  await expect(page.getByTestId('simulate-progress')).toBeVisible();

  // Wait for the best per world results to appear
  // Note: the loan-report.json has numbers as values, so React will render them as text.
  await expect(page.getByTestId('simulate-best-base')).toContainText('460');
  await expect(page.getByTestId('simulate-best-recession')).toContainText('202.5');
});
