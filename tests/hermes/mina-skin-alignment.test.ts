import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { PassThrough } from 'node:stream'
import { renderSync } from '@hermes/ink'
import React from 'react'
import { describe, expect, it } from 'vitest'
import { caduceus } from '../banner.js'
import { SessionPanel } from '../components/branding.js'
import { SessionPanel as LegacySessionPanel } from '../components/branding-before-loom.js'
import { fromSkin } from '../theme.js'
import type { Theme } from '../theme.js'
import type { SessionInfo } from '../types.js'

const skins = JSON.parse(readFileSync(process.env.LOOM_ALIGNMENT_SKINS!, 'utf8'))
const info: SessionInfo = {
  cwd: '/srv/loom/agents/mina',
  model: 'gpt-6-astra',
  tools: Object.fromEntries(Array.from({ length: 10 }, (_, i) => [`toolset${i}`, ['first', 'second']])),
  skills: { general: ['basecamp', 'github'] },
  version: '0.21.0',
  system_prompt: 'fixture prompt'
}
const theme = (skin: typeof skins[number]): Theme =>
  fromSkin(skin.colors, skin.branding, skin.banner_logo, skin.banner_hero)

async function frame(t: Theme, columns: number, tall: boolean, name?: string, legacy = false): Promise<string[]> {
  // The pinned Ink useStdout reads process.stdout, not renderSync's stream.
  const previousColumns = Object.getOwnPropertyDescriptor(process.stdout, 'columns')
  Object.defineProperty(process.stdout, 'columns', { configurable: true, value: columns })
  const stdout = new PassThrough()
  const stdin = new PassThrough()
  const stderr = new PassThrough()
  Object.assign(stdout, { columns, rows: 120, isTTY: false })
  Object.assign(stdin, { isTTY: false })
  Object.assign(stderr, { isTTY: false })
  let output = ''
  stdout.on('data', chunk => { output += chunk.toString() })
  const instance = renderSync(React.createElement(legacy ? LegacySessionPanel : SessionPanel, {
    info: { ...info, install_warning: tall ? Array.from({ length: 48 }, (_, i) => `warning-row-${i}`).join('\n') : undefined },
    sid: 'test-session', t
  }), { stdout, stdin, stderr, patchConsole: false } as any)
  try {
    await new Promise(resolve => setTimeout(resolve, 30))
    const plain = output.replace(/\u001b\[[0-?]*[ -/]*[@-~]/g, '').trimEnd()
    if (name && process.env.LOOM_ALIGNMENT_FRAMES) {
      writeFileSync(join(process.env.LOOM_ALIGNMENT_FRAMES, name + '.txt'), plain + '\n')
    }
    return plain.split('\n')
  } finally {
    instance.unmount()
    instance.cleanup()
    if (previousColumns) Object.defineProperty(process.stdout, 'columns', previousColumns)
    else delete process.stdout.columns
  }
}

describe('MINA opt-in centered session portrait', () => {
  for (const skin of skins) {
    const t = theme(skin)
    const art = caduceus(t.color, t.bannerHero).map(([, text]) => text)
    for (const columns of [80, 100, 120, 150, 180, 240]) {
      for (const tall of [false, true]) {
        it(`${skin.name}: ${columns} columns, ${tall ? 'tall' : 'short'} details`, async () => {
          const rows = await frame(t, columns, tall, `${skin.name}-${columns}-${tall ? 'tall' : 'short'}`)
          const width = Math.max(...art.map(line => line.length))
          const leftW = Math.min(width + 4, Math.floor(columns * 0.4))
          const wide = columns >= 90 && leftW + 40 < columns && width <= leftW
          expect(rows.some(row => row.includes('gpt-6-astra'))).toBe(true)
          expect(rows.some(row => row.includes('test-session'))).toBe(true)
          expect(rows.some(row => row.includes('/srv/loom/agents/mina'))).toBe(true)
          expect(rows.every(row => [...row].length <= columns)).toBe(true)
          if (!wide) {
            expect(rows.some(row => row.includes(art[0]))).toBe(false)
            return
          }
          const start = rows.findIndex(row => row.includes(art[0]))
          expect(start).toBeGreaterThanOrEqual(2)
          for (let i = 0; i < art.length; i++) expect(rows[start + i]).toContain(art[i])
          const x = rows[start].indexOf(art[0])
          // Border plus two padding cells, then balanced free space in the hero track.
          expect(Math.abs((x - 3) - (leftW - width - (x - 3)))).toBeLessThanOrEqual(1)
          const end = rows.findIndex(row => row.includes('Session: test-session'))
          const bottom = rows.findIndex(row => row.includes('\u2570'))
          const topGap = start - 2
          const bottomGap = bottom - 2 - end
          expect(Math.abs(topGap - bottomGap)).toBeLessThanOrEqual(1)
          if (tall) expect(topGap).toBeGreaterThan(0)
        })
      }
    }
  }

  it('does not enable alignment for missing or unknown branding values', async () => {
    const skin = skins[1]
    for (const alignment of [undefined, 'left', 'true']) {
      const t = theme({ ...skin, branding: { ...skin.branding, session_hero_alignment: alignment } })
      expect(t.brand.centerSessionHero).toBe(false)
      const rows = await frame(t, 180, true)
      const art = caduceus(t.color, t.bannerHero)
      expect(rows.findIndex(row => row.includes(art[0][1]))).toBe(2)
      expect(rows[2].indexOf(art[0][1])).toBe(3)
    }
  })

  for (const columns of [80, 100, 150, 240]) {
    it(`preserves complete non-opted-in frames at ${columns} columns`, async () => {
      for (const skin of skins) {
        const t = theme({ ...skin, branding: { ...skin.branding, session_hero_alignment: undefined } })
        expect(await frame(t, columns, true)).toEqual(await frame(t, columns, true, undefined, true))
      }
    })
  }

  it('includes alignment-only changes in the live theme equality predicate', () => {
    const source = readFileSync(new URL('../app/createGatewayEventHandler.ts', import.meta.url), 'utf8')
    const match = source.match(/const themesEqual = ([\s\S]*?)\n}\n/)
    expect(match).not.toBeNull()
    const body = match![1].replace('(a: Theme, b: Theme)', '(a, b)').replace(' as (keyof Theme[\'color\'])[]', '') + '\n}'
    const equal = new Function(`return (${body})`)()
    const t = theme(skins[0])
    expect(equal(t, { ...t })).toBe(true)
    expect(equal(t, { ...t, brand: { ...t.brand, centerSessionHero: false } })).toBe(false)
  })
})
