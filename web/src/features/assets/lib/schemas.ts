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
import type { TFunction } from 'i18next'
import { z } from 'zod'

import { trimAssetName } from './asset-source'
import {
  assetStorageProviders,
  normalizeAssetStorageEndpoint,
} from './storage-providers'

export function assetGroupSchema(t: TFunction) {
  return z.object({
    name: z
      .string()
      .trim()
      .min(1, t('Required'))
      .max(64, t('Name must not exceed 64 characters')),
  })
}

export function assetCreateSchema(t: TFunction) {
  return z
    .object({
      name: z
        .string()
        .transform(trimAssetName)
        .refine(
          (name) => [...name].length <= 64,
          t('Name must not exceed 64 characters')
        ),
      source: z.enum(['url', 'file']),
      url: z.string().trim(),
    })
    .superRefine((values, context) => {
      if (
        values.source === 'url' &&
        !z.url({ protocol: /^https?$/ }).safeParse(values.url).success
      ) {
        context.addIssue({
          code: 'custom',
          path: ['url'],
          message: t('Enter an HTTP or HTTPS URL'),
        })
      }
    })
}

export function assetStorageSchema(t: TFunction, hasSecret: boolean) {
  return z
    .object({
      enabled: z.boolean(),
      provider: z.enum(assetStorageProviders),
      endpoint: z.string().transform(normalizeAssetStorageEndpoint),
      region: z.string().trim(),
      bucket: z.string().trim(),
      access_key: z.string().trim(),
      secret_key: z.string(),
      force_path_style: z.boolean(),
      revision: z.number(),
    })
    .superRefine((values, context) => {
      if (!values.enabled && !values.endpoint) return
      for (const field of ['region', 'bucket', 'access_key'] as const) {
        if (!values[field]) {
          context.addIssue({
            code: 'custom',
            path: [field],
            message: t('Required'),
          })
        }
      }
      let validEndpoint = false
      try {
        const endpoint = new URL(values.endpoint)
        validEndpoint =
          /^https?:$/.test(endpoint.protocol) &&
          /^https?:\/\/[^\s/?#@\\]+\/?$/i.test(values.endpoint) &&
          endpoint.hostname !== ''
      } catch {
        // Keep the validation error attached to the endpoint field.
      }
      if (!validEndpoint) {
        context.addIssue({
          code: 'custom',
          path: ['endpoint'],
          message: t('Storage endpoint must be an HTTP or HTTPS origin'),
        })
      }
      if (!hasSecret && !values.secret_key) {
        context.addIssue({
          code: 'custom',
          path: ['secret_key'],
          message: t('Required'),
        })
      }
    })
}

export function assetChannelSchema(
  t: TFunction,
  hasSecret: boolean,
  multiKey: boolean
) {
  const sizeLimit = z
    .number()
    .int(t('Enter a non-negative whole number'))
    .min(0, t('Enter a non-negative whole number'))
    .max(9007199253, t('Value is too large'))
  return z
    .object({
      enabled: z.boolean(),
      endpoint: z.string().trim(),
      project_name: z
        .string()
        .trim()
        .max(64, t('Invalid upstream project name')),
      auth_type: z.enum(['bearer', 'aksk']),
      access_key: z.string().trim(),
      secret_key: z.string(),
      revision: z.number(),
      size_limits_mb: z.object({
        image: sizeLimit,
        video: sizeLimit,
        audio: sizeLimit,
      }),
    })
    .superRefine((values, context) => {
      if (!values.enabled) return
      if (multiKey) {
        context.addIssue({
          code: 'custom',
          path: ['enabled'],
          message: t('Asset libraries require a single-key channel.'),
        })
      }
      if (!z.url({ protocol: /^https$/ }).safeParse(values.endpoint).success) {
        context.addIssue({
          code: 'custom',
          path: ['endpoint'],
          message: t('Enter an HTTPS URL'),
        })
      }
      if (!values.project_name) {
        context.addIssue({
          code: 'custom',
          path: ['project_name'],
          message: t('Required'),
        })
      }
      if (values.auth_type === 'aksk') {
        if (!values.access_key) {
          context.addIssue({
            code: 'custom',
            path: ['access_key'],
            message: t('Required'),
          })
        }
        if (!hasSecret && !values.secret_key) {
          context.addIssue({
            code: 'custom',
            path: ['secret_key'],
            message: t('Required'),
          })
        }
      }
    })
}
