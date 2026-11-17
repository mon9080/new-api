/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  flexRender,
  getCoreRowModel,
  useReactTable,
} from '@tanstack/react-table'
import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { afterEach, expect, test } from 'vitest'

import { channelSchema, type Channel } from '../../types'
import { useChannelsColumns } from '../channels-columns'
import { ChannelsProvider, useChannels } from '../channels-provider'

let queryClient: QueryClient

afterEach(() => {
  queryClient.clear()
  localStorage.clear()
})

function NameCell(props: { channel: Channel }) {
  const { setSensitiveVisible } = useChannels()
  const [rowClicks, setRowClicks] = useState(0)
  const table = useReactTable({
    data: [props.channel],
    columns: useChannelsColumns({ enableSelection: false }),
    getCoreRowModel: getCoreRowModel(),
  })
  const cell = table
    .getRowModel()
    .rows[0].getAllCells()
    .find((item) => item.column.id === 'name')
  return (
    <>
      <button type='button' onClick={() => setSensitiveVisible(false)}>
        Hide names
      </button>
      <output aria-label='Row clicks'>{rowClicks}</output>
      <div onClick={() => setRowClicks((value) => value + 1)}>
        {cell && flexRender(cell.column.columnDef.cell, cell.getContext())}
      </div>
    </>
  )
}

function renderNameCell(baseUrl: string, vendorUrl: string) {
  queryClient = new QueryClient()
  const channel = channelSchema.parse({
    id: 1,
    type: 1,
    key: '',
    name: 'Reseller',
    status: 1,
    created_time: 0,
    test_time: 0,
    response_time: 0,
    balance_updated_time: 0,
    base_url: baseUrl,
    setting: JSON.stringify({ vendor_url: vendorUrl }),
  })
  render(
    <QueryClientProvider client={queryClient}>
      <ChannelsProvider>
        <NameCell channel={channel} />
      </ChannelsProvider>
    </QueryClientProvider>
  )
}

test('a channel with a vendor address exposes a keyboard accessible link without selecting its row', async () => {
  renderNameCell('http://localhost:8788', 'https://vendor.example/account')
  const user = userEvent.setup()
  const link = screen.getByRole('link', { name: 'Reseller' })
  expect(link).toHaveAttribute('href', 'https://vendor.example/account')
  expect(link).toHaveAttribute('target', '_blank')
  expect(link).toHaveAttribute('rel', 'noopener noreferrer')
  screen.getByRole('button', { name: 'Hide names' }).focus()
  await user.tab()
  expect(link).toHaveFocus()
  const click = new MouseEvent('click', { bubbles: true, cancelable: true })
  click.preventDefault()
  fireEvent(link, click)
  expect(screen.getByLabelText('Row clicks')).toHaveTextContent('0')
})

test('an invalid vendor address falls back to the channel Base URL', () => {
  renderNameCell('https://relay.example', 'javascript:alert(1)')
  expect(screen.getByRole('link', { name: 'Reseller' })).toHaveAttribute(
    'href',
    'https://relay.example'
  )
})

test('a channel without a safe address displays its name without a link', () => {
  renderNameCell('file:///private', 'data:text/html,hello')
  expect(screen.getByText('Reseller')).toBeVisible()
  expect(screen.queryByRole('link')).not.toBeInTheDocument()
})

test('hiding sensitive channel data removes the destination and channel name', async () => {
  renderNameCell('https://relay.example', 'https://vendor.example')
  await userEvent
    .setup()
    .click(screen.getByRole('button', { name: 'Hide names' }))
  expect(screen.queryByRole('link')).not.toBeInTheDocument()
  expect(screen.queryByText('Reseller')).not.toBeInTheDocument()
  expect(screen.getByText('••••')).toBeVisible()
})
