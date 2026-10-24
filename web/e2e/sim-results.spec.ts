import { test, expect } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';
import { fileURLToPath } from 'url';
import { dirname } from 'path';

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);

test('Simulation results display policy tree and heatmap per world', async ({ page }) => {
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

  await page.goto('/');
  await page.getByRole('combobox').selectOption({ label: 'Loan Underwriting (Simulation)' });
  
  await expect(page.getByTestId('sim-tab-experiment')).toBeVisible();
  await page.getByTestId('sim-tab-experiment').click();
  
  await expect(page.getByTestId('simulate-run')).toBeVisible();
  await page.getByTestId('simulate-run').click();

  await expect(page.getByTestId('sim-results')).toBeVisible();

  // Default world is 'base'
  await expect(page.getByTestId('results-world-base')).toBeVisible();
  await expect(page.getByTestId('result-choice-route-route[score,low]')).toContainText('approve');
  await expect(page.getByTestId('result-choice-route-route[score,high]')).toContainText('reject');

  // Heatmap check
  const aiScoreRouteMass = await page.locator('[data-testid="rf__edge-e2-ai_score-route"] path.react-flow__edge-path').evaluate((el) => window.getComputedStyle(el).strokeWidth);
  const screenRouteMass = await page.locator('[data-testid="rf__edge-e1-screen-route"] path.react-flow__edge-path').evaluate((el) => window.getComputedStyle(el).strokeWidth);
  expect(parseFloat(aiScoreRouteMass)).toBeGreaterThan(parseFloat(screenRouteMass));

  // Switch to recession
  await page.getByTestId('results-world-recession').click();
  await expect(page.getByTestId('result-choice-route-route[score,low]')).toContainText('review');
  await expect(page.getByTestId('result-choice-final-final[score,low,pass]')).toContainText('approve');
});
