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
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { ModelPricingExcelActions } from '../model-pricing-excel-actions'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

function renderActions() {
  const client = new QueryClient()
  const view = render(
    <QueryClientProvider client={client}>
      <ModelPricingExcelActions />
    </QueryClientProvider>
  )
  const input = view.container.querySelector('input[type="file"]')
  if (!(input instanceof HTMLInputElement)) {
    throw new Error('file input missing')
  }
  return input
}

const workbook = new File(['xlsx'], 'pricing.xlsx', {
  type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
})

const changes = [
  {
    model_name: 'gpt-4o',
    fields: [{ field: 'Model Ratio', before: '1.25', after: '' }],
  },
]

test('selecting a workbook with changes asks for confirmation before saving', async () => {
  const user = userEvent.setup()
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValueOnce({ data: { success: true, data: { changes } } })
    .mockResolvedValueOnce({
      data: { success: true, data: { applied: true, changes } },
    })
  const input = renderActions()

  await user.upload(input, workbook)

  const dialog = await screen.findByRole('alertdialog')
  expect(dialog).toHaveTextContent('gpt-4o')
  expect(dialog).toHaveTextContent('Model Ratio: 1.25 → (default)')
  expect(post).toHaveBeenCalledTimes(1)
  expect(post.mock.calls[0][2]).toEqual({ params: { dry_run: 'true' } })

  await user.click(screen.getByRole('button', { name: 'Import' }))

  await waitFor(() => expect(post).toHaveBeenCalledTimes(2))
  expect(post.mock.calls[1][2]).toEqual({ params: undefined })
  await waitFor(() =>
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  )
})

test('a workbook with invalid rows lists every error and saves nothing', async () => {
  const user = userEvent.setup()
  const post = vi.spyOn(api, 'post').mockResolvedValueOnce({
    data: {
      success: false,
      message: '2 row(s) have errors; nothing was saved',
      data: {
        errors: [
          'row 3 (gpt-4o): Billing Mode must be ratio, tiered_expr, or blank (got "expr")',
          'row 9 (claude-3): Model Ratio is not a number (got "abc")',
        ],
      },
    },
  })
  const input = renderActions()

  await user.upload(input, workbook)

  const dialog = await screen.findByRole('alertdialog')
  expect(dialog).toHaveTextContent('row 3 (gpt-4o)')
  expect(dialog).toHaveTextContent('row 9 (claude-3)')
  expect(
    screen.queryByRole('button', { name: 'Import' })
  ).not.toBeInTheDocument()
  expect(post).toHaveBeenCalledTimes(1)
})

test('a workbook without changes opens no confirmation', async () => {
  const user = userEvent.setup()
  const post = vi.spyOn(api, 'post').mockResolvedValueOnce({
    data: { success: true, data: { changes: [], unchanged: 12 } },
  })
  const input = renderActions()

  await user.upload(input, workbook)

  await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
})
