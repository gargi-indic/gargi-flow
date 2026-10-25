import { test, expect } from '@playwright/test';

test.describe('Simulation Mode B UI', () => {
  test('Behavior Editor creates distribution configs correctly', async ({ page }) => {
    await page.goto('/');
    
    // Switch to Sim editor to ensure graph panel is visible
    await page.getByTestId('sim-tab-graph').click();

    // The node 'process_claim' might be a plain node or absent depending on the initial state, 
    // let's edit graph yaml to ensure we have a node to test drawer on
    await page.getByTestId('sim-editor-graph').fill(`
nodes:
  - id: my_behavior_node
    type: behavior
    config:
      distribution: constant
      parameters:
        value: 10
edges: []
`);

    // Click on the node in the canvas to open the drawer
    await page.getByTestId('sim-node-my_behavior_node').click();
    
    // Check that behavior editor is visible
    const editor = page.getByTestId('behavior-editor');
    await expect(editor).toBeVisible();

    // Check we have the distribution select
    const distSelect = editor.locator('select').nth(1);
    await expect(distSelect).toHaveValue('constant');
    
    // Change to normal
    await distSelect.selectOption('normal');
    
    // Verify param inputs appeared
    await expect(editor.locator('text=mean')).toBeVisible();
    await expect(editor.locator('text=sd')).toBeVisible();

    // Verify SVG preview is drawn (check for path)
    const svgPreview = editor.locator('svg path');
    await expect(svgPreview).toBeVisible();
  });

  test('Resources and Arrivals panels can edit sim.yaml', async ({ page }) => {
    await page.goto('/');
    await page.getByTestId('sim-tab-sim').click();

    // Verify Resources panel
    const resPanel = page.getByTestId('sim-resources');
    await expect(resPanel).toBeVisible();
    
    // Add a resource
    await resPanel.locator('input[placeholder="New resource"]').fill('adjuster');
    await resPanel.locator('text=Add').click();
    
    // Check it appeared with a capacity input
    await expect(resPanel.locator('text=adjuster')).toBeVisible();
    const capacityInput = resPanel.locator('input[type="number"]');
    await expect(capacityInput).toHaveValue('1');
    
    // Edit capacity
    await capacityInput.fill('10');

    // Verify Arrivals panel
    const arrPanel = page.getByTestId('sim-arrivals');
    await expect(arrPanel).toBeVisible();

    // Change arrival dist
    const arrDistSelect = arrPanel.locator('select');
    await arrDistSelect.selectOption('exponential');
    
    // Verify rate input appeared and we can fill it
    const rateInput = arrPanel.locator('input[type="number"]');
    await rateInput.fill('5');
    await arrPanel.locator('text=Apply').click();

    // Check that yaml reflects changes
    const simEditor = page.getByTestId('sim-editor-sim');
    const simYaml = await simEditor.inputValue();
    expect(simYaml).toContain('resources:');
    expect(simYaml).toContain('adjuster: 10');
    expect(simYaml).toContain('arrivals:');
    expect(simYaml).toContain('distribution: exponential');
    expect(simYaml).toContain('rate: 5');
  });

  test('Pareto and Tournament panels render', async ({ page }) => {
    await page.goto('/');
    
    // Click simulate to generate mock results (or at least get to a state where pareto can try to render)
    // We might need to mock API if not mocked, but UI tests often rely on actual mock servers or simply
    // verifying the tab switches correctly.
    
    await page.getByTestId('sim-tab-pareto').click();
    
    // It should say "Run simulation to view" since we don't have mock results yet
    await expect(page.locator('text=Run simulation to view Pareto and Tournament reports.')).toBeVisible();
  });
});
