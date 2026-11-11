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
import { describe, expect, test } from 'vitest'

import { SORT_OPTIONS, FILTER_ALL } from '../constants'
import { filterAndSortModels, sortModels } from '../lib/filters'
import type { PricingModel } from '../types'

function pricingModel(
  modelName: string,
  overrides: Partial<PricingModel>
): PricingModel {
  return {
    id: 1,
    model_name: modelName,
    quota_type: 0,
    model_ratio: 1,
    completion_ratio: 1,
    enable_groups: ['default'],
    ...overrides,
  }
}

const models = [
  pricingModel('legacy-ratio', { model_ratio: 1, completion_ratio: 4 }),
  pricingModel('legacy-per-call', { quota_type: 1, model_price: 0.05 }),
  pricingModel('expr-tokens', {
    billing_mode: 'tiered_expr',
    billing_expr: 'tier("base", p * 1 + c * 10)',
  }),
  pricingModel('expr-long-context', {
    billing_mode: 'tiered_expr',
    billing_expr:
      'len <= 200000 ? tier("standard", p * 3 + c * 15) : tier("long", p * 6 + c * 22.5)',
  }),
  pricingModel('expr-input-only', {
    billing_mode: 'tiered_expr',
    billing_expr: 'tier("base", p * 0.1)',
  }),
  pricingModel('expr-fixed', {
    billing_mode: 'tiered_expr',
    billing_expr: 'tier("request", fixed(0.02))',
  }),
  pricingModel('expr-task', {
    billing_mode: 'tiered_expr',
    billing_expr: 'tier("base", u("seconds") * 0.1)',
    billing_usage_schema: { seconds: { type: 'number', unit: 'second' } },
    billing_usage_examples: [{ label: '5s', facts: { seconds: 5 } }],
  }),
]

describe('price sorting', () => {
  test.each([
    [
      SORT_OPTIONS.INPUT_PRICE_LOW,
      [
        'expr-input-only',
        'expr-tokens',
        'legacy-ratio',
        'expr-long-context',
        'legacy-per-call',
        'expr-fixed',
        'expr-task',
      ],
    ],
    [
      SORT_OPTIONS.INPUT_PRICE_HIGH,
      [
        'expr-long-context',
        'legacy-ratio',
        'expr-tokens',
        'expr-input-only',
        'legacy-per-call',
        'expr-fixed',
        'expr-task',
      ],
    ],
    [
      SORT_OPTIONS.OUTPUT_PRICE_LOW,
      [
        'legacy-ratio',
        'expr-tokens',
        'expr-long-context',
        'legacy-per-call',
        'expr-input-only',
        'expr-fixed',
        'expr-task',
      ],
    ],
    [
      SORT_OPTIONS.REQUEST_PRICE_HIGH,
      [
        'expr-task',
        'legacy-per-call',
        'expr-fixed',
        'legacy-ratio',
        'expr-tokens',
        'expr-long-context',
        'expr-input-only',
      ],
    ],
  ])(
    '%s compares base prices across legacy and expression billing and keeps unpriced models last',
    (sortBy, expected) => {
      expect(sortModels(models, sortBy).map((m) => m.model_name)).toEqual(
        expected
      )
    }
  )
})

const priceModes = [
  {
    name: 'legacy input',
    sort: SORT_OPTIONS.INPUT_PRICE_LOW,
    pricing: (price: number): Partial<PricingModel> => ({
      model_ratio: price / 2,
    }),
  },
  {
    name: 'legacy output',
    sort: SORT_OPTIONS.OUTPUT_PRICE_LOW,
    pricing: (price: number): Partial<PricingModel> => ({
      model_ratio: price / 2,
      completion_ratio: 1,
    }),
  },
  {
    name: 'legacy per-call',
    sort: SORT_OPTIONS.REQUEST_PRICE_LOW,
    pricing: (price: number): Partial<PricingModel> => ({
      quota_type: 1,
      model_price: price,
    }),
  },
  {
    name: 'expression input',
    sort: SORT_OPTIONS.INPUT_PRICE_LOW,
    pricing: (price: number): Partial<PricingModel> => ({
      billing_mode: 'tiered_expr',
      billing_expr: `tier("base", p * ${price} + c * ${price})`,
    }),
  },
  {
    name: 'expression output',
    sort: SORT_OPTIONS.OUTPUT_PRICE_LOW,
    pricing: (price: number): Partial<PricingModel> => ({
      billing_mode: 'tiered_expr',
      billing_expr: `tier("base", p * ${price} + c * ${price})`,
    }),
  },
  {
    name: 'expression fixed',
    sort: SORT_OPTIONS.REQUEST_PRICE_LOW,
    pricing: (price: number): Partial<PricingModel> => ({
      billing_mode: 'tiered_expr',
      billing_expr: `tier("base", fixed(${price}))`,
    }),
  },
  {
    name: 'task example',
    sort: SORT_OPTIONS.REQUEST_PRICE_LOW,
    pricing: (price: number): Partial<PricingModel> => ({
      billing_mode: 'tiered_expr',
      billing_expr: `tier("base", u("seconds") * ${price / 5})`,
      billing_usage_schema: { seconds: { type: 'number', unit: 'second' } },
      billing_usage_examples: [{ label: '5s', facts: { seconds: 5 } }],
    }),
  },
]

for (const rate of [0.1, 0]) {
  test.each(priceModes)(
    `sorts $name by the final rate ${rate}, reversing the base-price order`,
    ({ sort, pricing }) => {
      const discounted = pricingModel('expensive-base', {
        ...pricing(10),
        group_ratio: { default: 0.9 },
        effective_rates: {
          default: {
            model_name: 'expensive-base',
            source: 'user',
            rate,
            version: 1,
          },
        },
      })
      const regular = pricingModel('cheap-base', {
        ...pricing(2),
        group_ratio: { default: 0.9 },
        effective_rates: {
          default: {
            model_name: 'cheap-base',
            source: 'model',
            rate: 1,
            version: 1,
          },
        },
      })
      expect(
        sortModels([regular, discounted], sort, 'default').map(
          (model) => model.model_name
        )
      ).toEqual(['expensive-base', 'cheap-base'])
    }
  )
}

test.each([
  { group: 'default', expected: ['expensive-base', 'cheap-base'] },
  { group: 'vip', expected: ['cheap-base', 'expensive-base'] },
  { group: FILTER_ALL, expected: ['cheap-base', 'expensive-base'] },
])(
  'sorts the $group filter using inherited group rates when model rates are absent',
  ({ group, expected }) => {
    const discounted = pricingModel('expensive-base', {
      model_ratio: 5,
      enable_groups: ['default', 'vip'],
      group_ratio: { default: 0.1, vip: 1 },
    })
    const regular = pricingModel('cheap-base', {
      model_ratio: 1,
      enable_groups: ['default', 'vip'],
      group_ratio: { default: 1, vip: 0 },
    })
    expect(
      filterAndSortModels([regular, discounted], {
        search: '',
        vendor: FILTER_ALL,
        group,
        quotaType: FILTER_ALL,
        endpointType: FILTER_ALL,
        tag: FILTER_ALL,
        sortBy: SORT_OPTIONS.INPUT_PRICE_LOW,
      }).map((model) => model.model_name)
    ).toEqual(expected)
  }
)
