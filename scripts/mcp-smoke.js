// Starts the MCP server the way .mcp.json does (sh <plugin>/bin/specs-index mcp, no shell in between)
// and checks initialize, tools/list and one tools/call. Used by CI and by hand.
// Usage: node scripts/mcp-smoke.js PLUGIN_DIR PROJECT_DIR
const { spawn } = require('child_process')
const path = require('path')
const [plugin, project] = process.argv.slice(2)
if (!plugin || !project) { console.error('usage: mcp-smoke.js PLUGIN_DIR PROJECT_DIR'); process.exit(2) }
const launcher = path.join(path.resolve(plugin), 'bin', 'specs-index').split(path.sep).join('/')
const child = spawn('sh', [launcher, 'mcp'], {
  env: { ...process.env, CLAUDE_PROJECT_DIR: path.resolve(project) },
  stdio: ['pipe', 'pipe', 'inherit'],
})
const want = ['add_source', 'remove_source', 'index', 'search', 'show', 'trace', 'coverage', 'status']
const pending = new Map()
let buf = ''
child.stdout.on('data', (d) => {
  buf += d
  let i
  while ((i = buf.indexOf('\n')) >= 0) {
    const line = buf.slice(0, i).trim(); buf = buf.slice(i + 1)
    if (!line) continue
    const m = JSON.parse(line)
    if (pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id) }
  }
})
child.on('error', (e) => { console.error('mcp-smoke: cannot start sh:', e.message); process.exit(1) })
let nextId = 1
const call = (method, params) => new Promise((resolve, reject) => {
  const id = nextId++
  const t = setTimeout(() => reject(new Error(`no answer to ${method}`)), 60000)
  pending.set(id, (m) => { clearTimeout(t); resolve(m) })
  child.stdin.write(JSON.stringify({ jsonrpc: '2.0', id, method, params }) + '\n')
})
const fail = (msg) => { console.error('mcp-smoke: ' + msg); child.kill(); process.exit(1) }
;(async () => {
  const init = await call('initialize', { protocolVersion: '2025-11-25', capabilities: {}, clientInfo: { name: 'mcp-smoke', version: '0' } })
  if (!init.result || !init.result.serverInfo) fail('initialize failed: ' + JSON.stringify(init))
  console.log('initialize: protocolVersion=%s server=%s %s', init.result.protocolVersion, init.result.serverInfo.name, init.result.serverInfo.version)
  child.stdin.write(JSON.stringify({ jsonrpc: '2.0', method: 'notifications/initialized' }) + '\n')
  const list = await call('tools/list', {})
  const names = ((list.result || {}).tools || []).map((t) => t.name)
  for (const n of want) if (!names.includes(n)) fail(`tools/list lacks ${n}: ${names}`)
  console.log('tools/list: ' + names.join(', '))
  const st = await call('tools/call', { name: 'status', arguments: {} })
  if (!st.result || st.result.isError) fail('tools/call status failed: ' + JSON.stringify(st))
  console.log('tools/call status: ' + st.result.content[0].text.split('\n').slice(0, 3).join(' | '))
  child.stdin.end()
  console.log('mcp-smoke: ok')
})().catch((e) => fail(e.message))
