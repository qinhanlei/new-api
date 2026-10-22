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
import { z } from 'zod'

const name = z
  .string()
  .trim()
  .min(1, 'Name is required')
  .refine(
    (value) => new TextEncoder().encode(value).length <= 255,
    'Name is too long'
  )
export const assetFormSchema = z
  .object({
    name,
    source: z.enum(['file', 'url', 'rename']),
    file: z.custom<File | undefined>().optional(),
    url: z.string(),
  })
  .superRefine((value, context) => {
    if (value.source === 'file') {
      if (!value.file) {
        context.addIssue({
          code: 'custom',
          path: ['file'],
          message: 'Select a file',
        })
      } else if (value.file.size > 100 * 1024 * 1024) {
        context.addIssue({
          code: 'custom',
          path: ['file'],
          message: 'Asset exceeds the upload size limit',
        })
      }
    }
    if (value.source === 'url') {
      try {
        const url = new URL(value.url)
        if (
          !['http:', 'https:'].includes(url.protocol) ||
          url.username ||
          url.password ||
          value.url.length > 8192
        ) {
          throw new Error()
        }
      } catch {
        context.addIssue({
          code: 'custom',
          path: ['url'],
          message: 'Enter a valid HTTP or HTTPS URL',
        })
      }
    }
  })

export type AssetFormValues = z.infer<typeof assetFormSchema>
