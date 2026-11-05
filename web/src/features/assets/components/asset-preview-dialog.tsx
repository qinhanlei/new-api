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
import { useState } from 'react'

import { Dialog } from '@/components/dialog'

import type { Asset } from '../types'

export function AssetPreviewDialog(props: {
  asset: Asset
  onClose: () => void
}) {
  const [imageRatio, setImageRatio] = useState<number | null>(null)
  const isImage = props.asset.AssetType === 'Image'

  return (
    <Dialog
      open
      onOpenChange={(open) => !open && props.onClose()}
      title={props.asset.Name}
      contentClassName={
        isImage
          ? 'min-w-32 w-max [--preview-padding:calc(var(--spacing)*8)] [--preview-image-height:calc(var(--dialog-available-height,calc(100dvh-2rem))-var(--preview-padding)-var(--text-base)-var(--spacing)*4)] sm:max-w-[calc(100%-2rem)] sm:[--preview-padding:calc(var(--spacing)*12)]'
          : undefined
      }
      headerClassName={isImage ? '[contain:inline-size]' : undefined}
      titleClassName={isImage ? 'truncate pr-8' : undefined}
      bodyClassName={isImage ? 'flex justify-center py-0' : undefined}
    >
      {isImage && (
        <img
          src={props.asset.URL}
          alt={props.asset.Name}
          className='h-auto max-h-[var(--preview-image-height)] max-w-full object-contain'
          style={
            imageRatio
              ? {
                  // Reserve the panel padding, one-line title and gap. CSS
                  // keeps the image fitted when the viewport changes.
                  width: `min(calc(100vw - 2rem - var(--preview-padding)), calc(var(--preview-image-height) * ${imageRatio}))`,
                }
              : undefined
          }
          onLoad={(event) => {
            const image = event.currentTarget
            if (image.naturalWidth > 0 && image.naturalHeight > 0) {
              setImageRatio(image.naturalWidth / image.naturalHeight)
            }
          }}
          referrerPolicy='no-referrer'
        />
      )}
      {props.asset.AssetType === 'Video' && (
        <video
          src={props.asset.URL}
          controls
          preload='metadata'
          aria-label={props.asset.Name}
          className='max-h-[65vh] w-full'
        />
      )}
      {props.asset.AssetType === 'Audio' && (
        <audio
          src={props.asset.URL}
          controls
          preload='metadata'
          aria-label={props.asset.Name}
          className='w-full'
        />
      )}
    </Dialog>
  )
}
