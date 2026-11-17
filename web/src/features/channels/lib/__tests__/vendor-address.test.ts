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

import { channelSchema } from '../../types'
import {
  CHANNEL_FORM_DEFAULT_VALUES,
  transformChannelToFormDefaults,
  transformFormDataToCreatePayload,
  transformFormDataToUpdatePayload,
} from '../channel-form'
import { getChannelExternalLink } from '../channel-utils'

describe('channel vendor address', () => {
  test.each([
    [
      'https://relay.example',
      ' https://vendor.example/account ',
      'https://vendor.example/account',
    ],
    ['https://relay.example', 'http://vendor.example', 'http://vendor.example'],
    [' https://relay.example/v1 ', '', 'https://relay.example/v1'],
    ['https://relay.example', undefined, 'https://relay.example'],
    ['https://relay.example', 'javascript:alert(1)', 'https://relay.example'],
    ['https://relay.example', 'data:text/html,hello', 'https://relay.example'],
    ['https://relay.example', 'ftp://vendor.example', 'https://relay.example'],
    ['https://relay.example', 'not a URL', 'https://relay.example'],
    ['javascript:alert(1)', '', null],
    ['/relative', undefined, null],
    ['', '', null],
    [null, null, null],
  ])(
    'base %s and vendor %s select safe destination %s',
    (base, vendor, expected) => {
      expect(getChannelExternalLink(base, vendor)).toBe(expected)
    }
  )

  test('saving an edited vendor address preserves other setting and settings fields', () => {
    const channel = channelSchema.parse({
      id: 42,
      name: 'Reseller',
      type: 61,
      key: '',
      status: 1,
      created_time: 0,
      test_time: 0,
      response_time: 0,
      balance_updated_time: 0,
      setting: JSON.stringify({
        vendor_url: 'https://old-vendor.example',
        task_plugin_key: 'video-a',
        request_plugins: ['rp_aaaa1111'],
        custom_option: { enabled: true },
        http_protocol: 'http1',
        tls_insecure_skip_verify: true,
      }),
      settings: JSON.stringify({
        custom_setting: { region: 'eu' },
        plugin_config: { upload_url: 'https://upload.example' },
      }),
    })
    const form = transformChannelToFormDefaults(channel)
    expect(form.vendor_url).toBe('https://old-vendor.example')
    form.vendor_url = ' https://new-vendor.example/account '
    form.http_protocol = 'auto'
    form.tls_insecure_skip_verify = false

    for (const payload of [
      transformFormDataToCreatePayload(form).channel,
      transformFormDataToUpdatePayload(form, channel.id),
    ]) {
      const setting = JSON.parse(payload.setting ?? '{}')
      expect(setting).toMatchObject({
        vendor_url: 'https://new-vendor.example/account',
        task_plugin_key: 'video-a',
        request_plugins: ['rp_aaaa1111'],
        custom_option: { enabled: true },
      })
      expect(setting).not.toHaveProperty('http_protocol')
      expect(setting).not.toHaveProperty('tls_insecure_skip_verify')
      expect(JSON.parse(payload.settings ?? '{}')).toMatchObject({
        custom_setting: { region: 'eu' },
        plugin_config: { upload_url: 'https://upload.example' },
      })
      expect(
        transformChannelToFormDefaults({ ...channel, ...payload }).vendor_url
      ).toBe('https://new-vendor.example/account')
    }
  })

  test.each([undefined, 'not JSON', '{"vendor_url":42}'])(
    'setting %s defaults the vendor address to empty',
    (setting) => {
      const channel = channelSchema.parse({
        id: 42,
        name: 'Reseller',
        type: 1,
        key: '',
        status: 1,
        created_time: 0,
        test_time: 0,
        response_time: 0,
        balance_updated_time: 0,
        setting,
      })
      expect(transformChannelToFormDefaults(channel).vendor_url).toBe('')
    }
  )

  test('clearing the vendor address overwrites its saved value and falls back to Base URL', () => {
    const payload = transformFormDataToUpdatePayload(
      {
        ...CHANNEL_FORM_DEFAULT_VALUES,
        base_url: 'https://relay.example',
        setting: '{"vendor_url":"https://old-vendor.example"}',
        vendor_url: ' ',
      },
      42
    )
    const setting = JSON.parse(payload.setting ?? '{}')
    expect(setting.vendor_url).toBe('')
    expect(getChannelExternalLink(payload.base_url, setting.vendor_url)).toBe(
      'https://relay.example'
    )
  })
})
