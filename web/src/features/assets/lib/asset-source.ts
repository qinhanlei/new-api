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
import type { AssetType } from '../types'

export function trimAssetName(name: string): string {
  // Unicode White_Space matches the server's strings.TrimSpace (including NEL,
  // excluding BOM), unlike JavaScript's String.trim().
  return name.replaceAll(/^\p{White_Space}+|\p{White_Space}+$/gu, '')
}

export function deriveAssetName(source: {
  fileName?: string
  url?: string
}): string {
  let name = source.fileName ?? ''
  if (source.fileName === undefined && source.url) {
    // Keep the escaped path intact until its final segment is selected, just
    // like the server. URL() would normalize encoded dot segments first.
    const path = /^https?:\/\/[^/?#]*(\/[^?#]*)?/i.exec(source.url.trim())?.[1]
    const segment = path?.replace(/\/+$/, '').split('/').at(-1)
    if (!segment) return ''
    try {
      name = decodeURIComponent(segment)
    } catch {
      return ''
    }
  }
  const normalizedPath = name.replaceAll('\\', '/')
  name = normalizedPath.replace(/\/+$/, '').split('/').at(-1) ?? ''
  if (!name && normalizedPath.startsWith('/')) name = '/'
  name = trimAssetName(name.replaceAll(/\p{Cc}/gu, ''))
  if (!name || name === '.' || name === '..' || name === '/') return ''
  return [...name].slice(0, 64).join('')
}

/** Hints only: the upload request never sends this guess as AssetType. */
export function inspectAssetFile(file: File): {
  type?: AssetType
  unsupported: boolean
} {
  const mime = file.type.toLowerCase().split(';')[0].trim()
  if (
    /^(audio|video)\/(mp4|webm|ogg|(x-)?m4[abv])$/.test(mime) ||
    mime === 'application/ogg'
  ) {
    return { unsupported: false }
  }
  if (mime.startsWith('image/')) return { type: 'Image', unsupported: false }
  if (mime.startsWith('video/')) return { type: 'Video', unsupported: false }
  if (mime.startsWith('audio/')) return { type: 'Audio', unsupported: false }
  if (mime && mime !== 'application/octet-stream') {
    return { unsupported: true }
  }

  const extension = /\.([^.]+)$/.exec(file.name)?.[1].toLowerCase()
  if (
    extension &&
    /^(avif|bmp|gif|heic|heif|ico|jpe?g|png|tiff?|webp)$/.test(extension)
  ) {
    return { type: 'Image', unsupported: false }
  }
  if (extension && /^(aac|aiff?|flac|mp3|wav)$/.test(extension)) {
    return { type: 'Audio', unsupported: false }
  }
  if (extension && /^(avi|mkv|mov|mpeg|mpg)$/.test(extension)) {
    return { type: 'Video', unsupported: false }
  }
  // MP4, WebM and Ogg may contain either audio or video. Unknown containers
  // and extensions go to the server for content inspection.
  return {
    unsupported: Boolean(
      extension &&
      /^(apk|bat|cmd|csv|docx?|exe|html?|js|json|md|msi|pdf|pptx?|rar|sh|svg|tar|txt|xlsx?|xml|zip|7z)$/.test(
        extension
      )
    ),
  }
}
