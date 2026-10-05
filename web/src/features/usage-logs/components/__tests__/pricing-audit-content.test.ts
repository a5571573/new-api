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
import { createInstance } from 'i18next'
import { expect, test } from 'vitest'

import en from '@/i18n/locales/en.json'
import zhTW from '@/i18n/locales/zh-TW.json'

import { renderAuditContent } from '../../lib/format'

test.each<{
  action: string
  params: Record<string, number | string[]>
  english: string
  chinese: string
}>([
  {
    action: 'model.pricing.import',
    params: { count: 3, models: ['gpt-4o', 'qwen-plus', 'o3-mini'] },
    english: 'Imported model price list from Excel (updated 3 models)',
    chinese: '匯入價格表（Excel，更新 3 個模型價格）',
  },
  {
    action: 'model.pricing.export',
    params: { count: 266 },
    english: 'Exported model price list to Excel (266 models)',
    chinese: '匯出價格表（Excel，共 266 個模型）',
  },
  {
    action: 'model.pricing.update',
    params: { models: ['qwen-plus'] },
    english: 'Updated model prices',
    chinese: '更新模型價格',
  },
])(
  '$action audit entries describe the pricing operation in each language',
  async (scenario) => {
    const i18n = createInstance()
    await i18n.init({
      lng: 'en',
      resources: { en, zhTW },
      interpolation: { escapeValue: false },
    })
    const other = { op: { action: scenario.action, params: scenario.params } }

    expect(renderAuditContent(other, i18n.t)).toBe(scenario.english)
    await i18n.changeLanguage('zhTW')
    expect(renderAuditContent(other, i18n.t)).toBe(scenario.chinese)
  }
)
