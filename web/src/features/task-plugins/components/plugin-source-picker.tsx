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
import { FileCode2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { FilePicker } from '@/components/file-picker'

type PluginSourcePickerProps = {
  /** Name of the file whose text currently fills the source field, if any. */
  fileName: string
  onSelect: (file: File) => void
}

export function PluginSourcePicker(props: PluginSourcePickerProps) {
  const { t } = useTranslation()

  return (
    <FilePicker
      fileName={props.fileName}
      label={t('JavaScript file')}
      prompt={t('Drop a JavaScript plugin file here')}
      accept='.js,text/javascript'
      description={t(
        'Single .js file, up to 8 MiB. Its source is shown below before upload.'
      )}
      selectedIcon={
        <FileCode2
          aria-hidden='true'
          className='text-muted-foreground size-5'
        />
      }
      onSelect={(files) => {
        if (files[0]) props.onSelect(files[0])
      }}
    />
  )
}
