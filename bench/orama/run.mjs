// Config C of the specs-index benchmark: Orama (hybrid search) over the chunk
// texts exported by the Go runner, with Qwen3-Embedding 0.6B served by a local
// Ollama. Usage:
//   node run.mjs --chunks chunks.json --queries queries.json --out out.json
//     [--model qwen3-embedding:0.6b] [--ollama http://127.0.0.1:11434]
//     [--repeats 20] [--warmup 1]
// Orama API as defined by the installed @orama/orama types (create, insertMultiple,
// search with mode "hybrid", term + vector {value, property}, similarity, limit; save).
import { readFileSync, writeFileSync } from 'node:fs'
import { create, insertMultiple, search, save } from '@orama/orama'

const args = {}
for (let i = 2; i < process.argv.length; i += 2) args[process.argv[i].replace(/^--/, '')] = process.argv[i + 1]
const model = args.model ?? 'qwen3-embedding:0.6b'
const ollama = (args.ollama ?? 'http://127.0.0.1:11434').replace(/\/$/, '')
const repeats = Number(args.repeats ?? 20)
const warmup = Number(args.warmup ?? 1)
const K = 10
if (!args.chunks || !args.queries || !args.out) {
  console.error('need --chunks, --queries and --out')
  process.exit(2)
}

async function embed(texts) {
  const r = await fetch(`${ollama}/api/embed`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ model, input: texts }),
  })
  if (!r.ok) throw new Error(`ollama /api/embed: HTTP ${r.status} ${await r.text()}`)
  const j = await r.json()
  if (!Array.isArray(j.embeddings) || j.embeddings.length !== texts.length) throw new Error('ollama returned a wrong number of embeddings')
  return j.embeddings
}

const chunks = JSON.parse(readFileSync(args.chunks, 'utf8')) // [{key, text}]
const queries = JSON.parse(readFileSync(args.queries, 'utf8')).queries

// Embed every chunk text as-is (no prefix), 32 per request like the Go tool.
const t0 = performance.now()
const vectors = []
for (let i = 0; i < chunks.length; i += 32) {
  vectors.push(...(await embed(chunks.slice(i, i + 32).map((c) => c.text))))
}
const embedSeconds = (performance.now() - t0) / 1000
const dims = vectors[0].length

const t1 = performance.now()
const db = create({ schema: { key: 'string', text: 'string', embedding: `vector[${dims}]` } })
await insertMultiple(db, chunks.map((c, i) => ({ key: c.key, text: c.text, embedding: vectors[i] })))
const indexSeconds = (performance.now() - t1) / 1000
const serializedBytes = Buffer.byteLength(JSON.stringify(save(db)))

const skipped = {}
const rankings = {}
const samples = []
const runnable = queries.filter((q) => {
  if (q.category === 'filtered') {
    skipped[q.q] = 'the Orama harness has no filter language (type:, status:, tag:, has:)'
    return false
  }
  return true
})
for (let pass = 0; pass < warmup + repeats; pass++) {
  for (const q of runnable) {
    const s = performance.now()
    const [vec] = await embed([q.q])
    const res = await search(db, {
      mode: 'hybrid', term: q.q, vector: { value: vec, property: 'embedding' }, similarity: 0, limit: K, includeVectors: false,
    })
    const ms = performance.now() - s
    if (pass === 0) rankings[q.q] = res.hits.map((h) => h.document.key)
    if (pass >= warmup) samples.push(ms)
  }
}
writeFileSync(args.out, JSON.stringify({
  orama_version: JSON.parse(readFileSync(new URL('./node_modules/@orama/orama/package.json', import.meta.url), 'utf8')).version,
  node_version: process.version, model, dims, chunks: chunks.length,
  embed_seconds: embedSeconds, index_seconds: indexSeconds, serialized_bytes: serializedBytes,
  repeats, warmup, latencies_ms: samples, rankings, skipped,
}))
