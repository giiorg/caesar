import { screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { renderApp } from '../../test/render-app'

describe('/more', () => {
  it('reaches exactly the pages the tab bar dropped', async () => {
    // The five-tab bar is only defensible if the pages it drops stay reachable
    // from a phone. This is what makes More a route rather than a label:
    // without it, Workflows, Repos and Admin live on the desktop rail alone and
    // the phone silently loses three pages.
    //
    // EXACT and ordered rather than a presence check per label, because the
    // failure worth catching is a page ADDED to the rail and not to More — a
    // presence loop cannot see one.
    renderApp('/more')
    // Scoped to <main>, and that is not incidental: the desktop rail renders
    // the same three labels in the same document (jsdom applies no media
    // queries), so an unscoped role query finds two of each and throws.
    const main = within(await screen.findByRole('main'))
    await main.findByRole('link', { name: 'Workflows' })
    expect(main.getAllByRole('link').map((l) => [l.textContent, l.getAttribute('href')])).toEqual([
      ['Workflows', '/workflows'],
      ['Repos', '/repos'],
      ['Admin', '/admin'],
    ])
  })
})
