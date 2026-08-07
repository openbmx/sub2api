import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
}))

vi.mock('../client', () => ({
  apiClient: {
    get,
    post,
  },
}))

import {
  getRollbackVersions,
  performUpdate,
  rollback,
  UPDATE_REQUEST_TIMEOUT_MS,
  type RollbackVersionInfo,
} from '@/api/admin/system'

describe('admin system rollback API', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
  })

  it('getRollbackVersions fetches the rollback version list', async () => {
    const versions: RollbackVersionInfo[] = [
      {
        version: '0.1.146',
        published_at: '2026-07-07T00:00:00Z',
        html_url: 'https://github.com/openbmx/sub2api/releases/tag/v0.1.146'
      }
    ]
    get.mockResolvedValue({ data: { versions } })

    const result = await getRollbackVersions()

    expect(get).toHaveBeenCalledWith('/admin/system/rollback-versions')
    expect(result.versions).toEqual(versions)
  })

  it('rollback posts the target version in the request body', async () => {
    post.mockResolvedValue({ data: { message: 'ok', need_restart: true } })

    const result = await rollback('0.1.146')

    // The third argument carries the extended timeout added in #4504: a release
    // download can outlast the global 30s axios timeout, so it must stay asserted.
    expect(post).toHaveBeenCalledWith(
      '/admin/system/rollback',
      { version: '0.1.146' },
      expect.objectContaining({ timeout: UPDATE_REQUEST_TIMEOUT_MS }),
    )
    expect(result.need_restart).toBe(true)
  })

  it('rollback without a version posts no body (legacy backup rollback)', async () => {
    post.mockResolvedValue({ data: { message: 'ok', need_restart: true } })

    await rollback()

    expect(post).toHaveBeenCalledWith(
      '/admin/system/rollback',
      undefined,
      expect.objectContaining({ timeout: UPDATE_REQUEST_TIMEOUT_MS }),
    )
  })

  it('performUpdate opts out of the global axios timeout (#4504)', async () => {
    post.mockResolvedValue({ data: { message: 'ok', need_restart: true } })

    await performUpdate()

    expect(post).toHaveBeenCalledWith(
      '/admin/system/update',
      undefined,
      expect.objectContaining({ timeout: UPDATE_REQUEST_TIMEOUT_MS }),
    )
  })
})
