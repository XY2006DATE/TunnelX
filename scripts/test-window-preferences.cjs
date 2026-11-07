// Headless frontend tests with a fake Tauri bridge; never controls a desktop app.
const assert = require('node:assert/strict');
const http = require('node:http');
const fs = require('node:fs/promises');
const path = require('node:path');
const {chromium} = require(process.env.TUNNELX_PLAYWRIGHT_PATH || 'playwright');
const root = path.resolve(__dirname, '..');
const output = process.env.TUNNELX_TEST_OUTPUT ? path.resolve(process.env.TUNNELX_TEST_OUTPUT) : path.join(root, 'dist/test-results/0.2.0-close-preferences');

async function serve(directory) {
  const server = http.createServer(async (request, response) => {
    const pathname = decodeURIComponent(new URL(request.url, 'http://localhost').pathname);
    const file = path.resolve(directory, '.' + (pathname === '/' ? '/index.html' : pathname));
    if (!file.startsWith(directory + path.sep)) {response.writeHead(403).end(); return;}
    try {
      const data = await fs.readFile(file);
      response.setHeader('Content-Type', {'.html':'text/html', '.js':'text/javascript', '.css':'text/css', '.svg':'image/svg+xml'}[path.extname(file)] || 'application/octet-stream');
      response.end(data);
    } catch {response.writeHead(404).end();}
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  return {server, url: `http://127.0.0.1:${server.address().port}`};
}

function fakeTauri({supported = true, startupClose = true}) {
  const callbacks = new Map(), listeners = new Map();
  let id = 0;
  const state = {
    behavior: localStorage.getItem('mock-close-behavior') || 'ask',
    pending: supported && startupClose && !sessionStorage.getItem('mock-started'),
    calls: [], failSave: false,
  };
  sessionStorage.setItem('mock-started', 'true');
  window.__test = state;
  window.__TAURI_EVENT_PLUGIN_INTERNALS__ = {unregisterListener: (_event, eventId) => listeners.delete(eventId)};
  window.__TAURI_INTERNALS__ = {
    transformCallback: callback => {callbacks.set(++id, callback); return id;},
    invoke: async (command, args = {}) => {
      state.calls.push({command, args});
      if (command === 'plugin:event|listen') {listeners.set(++id, args); return id;}
      if (command === 'plugin:event|unlisten') {listeners.delete(args.eventId); return;}
      if (command === 'window_preferences') return supported ? {close_behavior: state.behavior, close_requested: state.pending} : null;
      if (command === 'set_close_behavior') {
        if (state.failSave) throw new Error('Simulated preference write failure');
        state.behavior = args.behavior;
        localStorage.setItem('mock-close-behavior', state.behavior);
        return;
      }
      if (command === 'resolve_close_request') {
        if (state.failSave && args.remember && args.decision !== 'cancel') throw new Error('Simulated preference write failure');
        if (args.remember && args.decision !== 'cancel') {
          state.behavior = args.decision;
          localStorage.setItem('mock-close-behavior', state.behavior);
        }
        state.pending = false;
        return;
      }
      if (command === 'dashboard_port') return 7101;
      if (command === 'login') return true;
      if (command === 'api_request') {
        if (args.path === '/api/setup-status') return {configured:true};
        if (args.path === '/login') return {success:true};
        if (args.path === '/api/stats') return {};
        if (args.path === '/api/token') return {token:'synthetic-test-token'};
        if (args.path === '/api/runtime-config') return {port:7100, tls_enabled:false};
        return [];
      }
      throw new Error('Unexpected test command: ' + command);
    },
  };
  state.emitClose = () => {
    state.pending = true;
    for (const listener of listeners.values()) {
      if (listener.event === 'desktop-close-requested') callbacks.get(listener.handler)?.({event:listener.event, payload:null});
    }
  };
}

async function openSettings(page) {
  await page.locator('input[name="password"]').fill('synthetic-test-password');
  await page.getByRole('button', {name:'登录', exact:true}).click();
  await page.getByRole('button', {name:'设置', exact:true}).last().click();
}

async function testComponent(browser, component) {
  const {server, url} = await serve(path.join(root, component, 'dashboard/web'));
  const context = await browser.newContext({viewport:{width:1280, height:820}});
  try {
    const page = await context.newPage();
    const errors = [];
    page.on('pageerror', error => errors.push(String(error)));
    await page.route('**/*', route => route.request().url().startsWith(url) ? route.continue() : route.abort());
    await page.addInitScript(fakeTauri, {});
    await page.goto(url);
    const dialog = page.getByRole('dialog');
    await dialog.waitFor();
    assert.equal(await dialog.getByRole('checkbox').isChecked(), true);
    assert.equal(await page.evaluate(() => document.activeElement.textContent), '隐藏到托盘');
    await page.screenshot({path:path.join(output, component + '-first-close.png')});
    await page.keyboard.press('Tab');
    assert.equal(await dialog.getByRole('checkbox').evaluate(element => element === document.activeElement), true);
    await page.keyboard.press('Escape');
    await dialog.waitFor({state:'hidden'});
    assert.equal(await page.evaluate(() => window.__test.behavior), 'ask');
    console.log(`PASS ${component}: startup close prompt before login, focus trap, Escape cancels`);

    await page.evaluate(() => window.__test.emitClose());
    await dialog.getByRole('checkbox').uncheck();
    await dialog.getByRole('button', {name:'隐藏到托盘', exact:true}).click();
    await dialog.waitFor({state:'hidden'});
    assert.equal(await page.evaluate(() => window.__test.behavior), 'ask');
    const choice = await page.evaluate(() => window.__test.calls.filter(c => c.command === 'resolve_close_request').at(-1).args);
    assert.deepEqual(choice, {decision:'tray', remember:false});
    console.log(`PASS ${component}: unremembered tray choice leaves future prompting enabled`);

    await openSettings(page);
    const setting = page.getByRole('combobox', {name:'关闭窗口时', exact:true});
    await setting.selectOption('exit');
    await page.waitForFunction(() => window.__test.behavior === 'exit');
    await page.reload();
    await openSettings(page);
    assert.equal(await setting.inputValue(), 'exit');
    await page.evaluate(() => {window.__test.failSave = true;});
    await setting.selectOption('tray');
    await page.getByRole('alert').filter({hasText:'Simulated preference write failure'}).waitFor();
    assert.equal(await setting.inputValue(), 'exit');
    await page.evaluate(() => {window.__test.failSave = false;});
    await setting.selectOption('ask');
    await page.waitForFunction(() => window.__test.behavior === 'ask');
    await page.screenshot({path:path.join(output, component + '-close-setting.png')});
    console.log(`PASS ${component}: settings persist across reload, write failure preserves selection, Ask can be restored`);

    await page.evaluate(() => window.__test.emitClose());
    await dialog.getByRole('button', {name:'退出程序', exact:true}).click();
    await dialog.waitFor({state:'hidden'});
    assert.equal(await setting.inputValue(), 'exit');
    const remembered = await page.evaluate(() => window.__test.calls.filter(c => c.command === 'resolve_close_request').at(-1).args);
    assert.deepEqual(remembered, {decision:'exit', remember:true});
    assert.deepEqual(errors, []);
    console.log(`PASS ${component}: remembered Quit updates settings through the same preference state`);

    if (component === 'client') {
      await page.getByRole('button', {name:'概览', exact:true}).last().click();
      await page.getByRole('button', {name:'申请代理', exact:true}).click();
      await page.locator('input[name="server_addr"]').fill('https://control.example.com/team/proxy?tenant=one');
      await page.locator('input[name="token"]').fill('synthetic-route-token');
      await page.locator('input[name="local_port"]').fill('8080');
      await page.getByRole('button', {name:'提交申请', exact:true}).click();
      await page.waitForFunction(() => window.__test.calls.some(c => c.command === 'api_request' && c.args.path === '/api/request-proxy'));
      const request = await page.evaluate(() => window.__test.calls.filter(c => c.command === 'api_request' && c.args.path === '/api/request-proxy').at(-1).args.body);
      assert.equal(request.server_addr, 'wss://control.example.com/team/proxy?tenant=one');
      assert.equal(request.server_port, 443);
      assert.equal(request.server_tls, true);
      assert.equal(request.proxy_type, 'tcp');
      console.log('PASS client: route submission preserves full WebSocket URL, path and query');
    }

    const unsupported = await browser.newContext();
    try {
      const web = await unsupported.newPage();
      await web.addInitScript(fakeTauri, {supported:false, startupClose:false});
      await web.goto(url);
      await openSettings(web);
      assert.equal(await web.getByRole('combobox', {name:'关闭窗口时', exact:true}).count(), 0);
      console.log(`PASS ${component}: unsupported desktop platforms hide Windows-only preference`);
    } finally {await unsupported.close();}
  } finally {await context.close(); await new Promise(resolve => server.close(resolve));}
}

(async () => {
  await fs.mkdir(output, {recursive:true});
  const browser = await chromium.launch({channel:process.env.TUNNELX_BROWSER_CHANNEL || 'msedge', headless:true});
  try {for (const component of ['client','server']) await testComponent(browser, component);}
  finally {await browser.close();}
  console.log('ALL WINDOW PREFERENCE FRONTEND TESTS PASSED');
})().catch(error => {console.error(error); process.exitCode = 1;});
