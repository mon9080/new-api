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
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import type { Channel } from '../../types'
import {
  CHANNEL_FORM_DEFAULT_VALUES,
  channelFormSchema,
  transformChannelToFormDefaults,
  transformFormDataToUpdatePayload,
} from '../channel-form'

const CAPACITY_ERROR_MESSAGE =
  'Selected model is at capacity. Please try a different model.'

function openAIChannel(settings: Record<string, unknown>): Channel {
  return {
    id: 1,
    name: 'Codex upstream',
    type: 1,
    models: 'gpt-5',
    group: 'default',
    status: 1,
    channel_info: {
      is_multi_key: false,
      multi_key_size: 0,
      multi_key_polling_index: 0,
      multi_key_mode: 'random',
    },
    settings: JSON.stringify(settings),
  } as Channel
}

describe('Codex response guard channel settings', () => {
  test('old enabled settings load the capacity matcher and five retries', () => {
    const defaults = transformChannelToFormDefaults(
      openAIChannel({ codex_response_guard_enabled: true })
    )

    assert.equal(defaults.codex_response_guard_status_codes, '')
    assert.equal(
      defaults.codex_response_guard_error_messages,
      CAPACITY_ERROR_MESSAGE
    )
    assert.equal(defaults.codex_response_guard_retry_times, 5)
  })

  test('disabled guard keeps saved matcher values when the channel is saved', () => {
    const channel = openAIChannel({
      codex_response_guard_enabled: false,
      codex_response_guard_status_codes: '429,500-503',
      codex_response_guard_error_messages: `${CAPACITY_ERROR_MESSAGE}\nServer overloaded`,
      codex_response_guard_retry_times: 9,
    })

    const defaults = transformChannelToFormDefaults(channel)
    const payload = transformFormDataToUpdatePayload(defaults, channel.id)
    const savedSettings = JSON.parse(String(payload.settings))

    assert.equal(defaults.codex_response_guard_enabled, false)
    assert.equal(defaults.codex_response_guard_status_codes, '429,500-503')
    assert.equal(
      defaults.codex_response_guard_error_messages,
      `${CAPACITY_ERROR_MESSAGE}\nServer overloaded`
    )
    assert.equal(defaults.codex_response_guard_retry_times, 9)
    assert.equal(savedSettings.codex_response_guard_enabled, false)
    assert.equal(savedSettings.codex_response_guard_status_codes, '429,500-503')
    assert.equal(
      savedSettings.codex_response_guard_error_messages,
      `${CAPACITY_ERROR_MESSAGE}\nServer overloaded`
    )
    assert.equal(savedSettings.codex_response_guard_retry_times, 9)
  })

  test('an explicitly cleared error matcher stays empty after reloading', () => {
    const defaults = transformChannelToFormDefaults(
      openAIChannel({
        codex_response_guard_enabled: true,
        codex_response_guard_error_messages: '',
        codex_response_guard_retry_times: 5,
      })
    )

    assert.equal(defaults.codex_response_guard_error_messages, '')
  })

  test('saved HTTP status code 516 is preserved independently of the built-in rule', () => {
    const defaults = transformChannelToFormDefaults(
      openAIChannel({
        codex_response_guard_enabled: true,
        codex_response_guard_status_codes: '429,516,500-503',
      })
    )

    assert.equal(defaults.codex_response_guard_status_codes, '429,516,500-503')
  })

  test('status matcher accepts optional 4xx and 5xx values and ranges only', () => {
    const parseStatusCodes = (value: string) =>
      channelFormSchema.safeParse({
        ...CHANNEL_FORM_DEFAULT_VALUES,
        name: 'Codex upstream',
        models: 'gpt-5',
        codex_response_guard_status_codes: value,
      }).success

    assert.equal(parseStatusCodes(''), true)
    assert.equal(parseStatusCodes('429,500-503'), true)
    assert.equal(parseStatusCodes('399'), false)
    assert.equal(parseStatusCodes('500-429'), false)
    assert.equal(parseStatusCodes('capacity'), false)
  })

  test('retry count accepts the configured 1-20 range only', () => {
    const parseRetryTimes = (value: number) =>
      channelFormSchema.safeParse({
        ...CHANNEL_FORM_DEFAULT_VALUES,
        name: 'Codex upstream',
        models: 'gpt-5',
        codex_response_guard_retry_times: value,
      }).success

    assert.equal(parseRetryTimes(1), true)
    assert.equal(parseRetryTimes(20), true)
    assert.equal(parseRetryTimes(0), false)
    assert.equal(parseRetryTimes(21), false)
    assert.equal(parseRetryTimes(1.5), false)
  })
})
