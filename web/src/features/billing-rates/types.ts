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
export type BillingRateSource = 'user' | 'model' | 'group_special' | 'group'

export type BillingRateSnapshot = {
  rate: number
  source: BillingRateSource
  version: number
  model_name: string
}

export type BillingRateRule = {
  model_name: string
  rate: string | null
  version: number
}

export type BillingRateConfig = {
  billing_model_name?: string
  rules: BillingRateRule[]
  global_rules?: BillingRateRule[]
}

export type BillingRateChange = {
  model_name: string
  rate: string | null
  expected_version: number
}
