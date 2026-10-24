import { test, expect } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';
import { fileURLToPath } from 'url';
import { dirname } from 'path';

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);

test('Simulation analysis panels: regret, voi, flip point, compare and run replay', async ({ page }) => {
  await page.route('**/v1/models', async (route) => {
    if (route.request().method() === 'POST') {
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ model: { id: 'loan-underwriting', version: '1' } }) });
    } else {
      await route.fallback();
    }
  });

  await page.route('**/v1/experiments', async (route) => {
    if (route.request().method() === 'POST') {
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ id: 'exp-1', status: 'EXPERIMENT_STATUS_QUEUED' }) });
    } else {
      await route.fallback();
    }
  });

  await page.route('**/v1/experiments/exp-1/events', async (route) => {
    const sseBody = `id: 1\nevent: progress\ndata: {"id":"exp-1","sequence":"1","eventType":"progress","status":"EXPERIMENT_STATUS_RUNNING","progress":{"done":"84","total":"84","best":"loan-underwriting@exhaustive-b1b8bd20353bca7f","bestMean":460}}\n\nid: 2\nevent: done\ndata: {"id":"exp-1","sequence":"2","eventType":"done","status":"EXPERIMENT_STATUS_SUCCEEDED","progress":{"done":"84","total":"84","best":"loan-underwriting@exhaustive-b1b8bd20353bca7f","bestMean":460}}\n\n`;
    await route.fulfill({ status: 200, contentType: 'text/event-stream', body: sseBody });
  });

  await page.route('**/v1/experiments/exp-1/reports/summary', async (route) => {
    const summaryData = JSON.parse(fs.readFileSync(path.join(__dirname, 'fixtures', 'loan-report.json'), 'utf8'));
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(summaryData) });
  });

  await page.route('**/v1/experiments/exp-1/runs/0*', async (route) => {
    const runData = JSON.parse(fs.readFileSync(path.join(__dirname, 'fixtures', 'loan-run-0.json'), 'utf8'));
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(runData) });
  });

  await page.goto('/');
  await page.getByRole('combobox').selectOption({ label: 'Loan Underwriting (Simulation)' });

  // Run the experiment from Simulate panel
  await expect(page.getByTestId('sim-tab-experiment')).toBeVisible();
  await page.getByTestId('sim-tab-experiment').click();

  await expect(page.getByTestId('simulate-run')).toBeVisible();
  await page.getByTestId('simulate-run').click();

  // Wait for experiment completion and results bar
  await expect(page.getByTestId('sim-results')).toBeVisible();

  // 1. Regret Panel
  await page.getByTestId('sim-tab-regret').click();
  await expect(page.getByTestId('sim-regret')).toBeVisible();
  await expect(page.getByTestId('sim-regret')).toContainText('267.5');
  await expect(page.getByTestId('sim-regret')).toContainText('142.2');

  // 2. VOI Panel
  await page.getByTestId('sim-tab-voi').click();
  await expect(page.getByTestId('sim-voi')).toBeVisible();
  await expect(page.getByTestId('voi-node-ai_score').first()).toBeVisible();

  // 3. Flip Point Panel
  await page.getByTestId('sim-tab-flip').click();
  await expect(page.getByTestId('sim-flip-point')).toBeVisible();
  await expect(page.getByTestId('sim-flip-point')).toContainText('p_bad');

  // 4. Compare Panel
  await page.getByTestId('sim-tab-compare').click();
  await expect(page.getByTestId('sim-compare')).toBeVisible();

  // 5. Replay Panel
  await page.getByTestId('sim-tab-replay').click();
  await expect(page.getByTestId('sim-replay')).toBeVisible();

  await expect(page.getByTestId('replay-fetch')).toBeVisible();
  await page.getByTestId('replay-fetch').click();

  await expect(page.getByTestId('replay-step')).toContainText('Step 0 / 16');

  // Step through events
  await page.getByTestId('replay-next').click();
  await expect(page.getByTestId('replay-step')).toContainText('Step 1 / 16');

  await page.getByTestId('replay-next').click();
  await expect(page.getByTestId('replay-step')).toContainText('Step 2 / 16');

  await page.getByTestId('replay-next').click();
  await expect(page.getByTestId('replay-step')).toContainText('Step 3 / 16');

  // After step 3, node "screen" reached SUCCEEDED
  await expect(page.getByTestId('sim-node-screen')).toBeVisible();
});
