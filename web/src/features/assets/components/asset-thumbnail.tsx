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
import { File, FileImage, FileMusic, FileVideo, ImageOff } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'

import type { Asset } from '../types'

export function AssetThumbnail(props: {
  asset: Asset
  onPreview: (asset: Asset) => void
}) {
  const { t } = useTranslation()
  const url = /^https?:\/\//i.test(props.asset.URL) ? props.asset.URL : ''
  const [image, setImage] = useState({
    url,
    loaded: false,
    failed: false,
    retried: false,
  })

  // Polling renews signatures for immutable originals. Keep a working image;
  // only try a newer signature once after a load failure.
  if (
    url &&
    url !== image.url &&
    (!image.url || (image.failed && !image.retried))
  ) {
    setImage({ url, loaded: false, failed: false, retried: Boolean(image.url) })
  }

  let placeholder = <File aria-hidden='true' />
  if (props.asset.AssetType === 'Image') {
    placeholder = <FileImage aria-hidden='true' />
  }
  if (props.asset.AssetType === 'Video') {
    placeholder = <FileVideo aria-hidden='true' />
  }
  if (props.asset.AssetType === 'Audio') {
    placeholder = <FileMusic aria-hidden='true' />
  }
  if (image.failed && props.asset.AssetType === 'Image') {
    placeholder = <ImageOff aria-hidden='true' />
  }
  const showImage =
    props.asset.AssetType === 'Image' &&
    Boolean(url && image.url) &&
    !image.failed

  return (
    <Button
      variant='outline'
      size='icon'
      className='relative size-12 shrink-0 overflow-hidden p-0'
      aria-label={t('Preview {{name}}', { name: props.asset.Name })}
      title={image.failed ? t('Image not available') : undefined}
      disabled={!url || !props.asset.AssetType}
      onClick={() => props.onPreview(props.asset)}
    >
      {showImage ? (
        <>
          {!image.loaded && (
            <Skeleton className='absolute inset-0 rounded-none' />
          )}
          <img
            src={image.url}
            alt={props.asset.Name}
            width={48}
            height={48}
            loading='lazy'
            decoding='async'
            referrerPolicy='no-referrer'
            className='relative size-full object-contain'
            onLoad={() =>
              setImage((previous) => ({ ...previous, loaded: true }))
            }
            onError={() =>
              setImage((previous) => ({ ...previous, failed: true }))
            }
          />
        </>
      ) : (
        placeholder
      )}
    </Button>
  )
}
