import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

const mocks = vi.hoisted(() => ({
  showError: vi.fn(),
  showSuccess: vi.fn(),
  batchUpdateBalance: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: { users: { batchUpdateBalance: mocks.batchUpdateBalance } },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: mocks.showError, showSuccess: mocks.showSuccess }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

vi.mock('@/components/common/BaseDialog.vue', () => ({
  default: {
    name: 'BaseDialog',
    props: ['show', 'title', 'width'],
    template: '<div v-if="show"><slot /><slot name="footer" /></div>',
  },
}))

import BulkBalanceModal from '../BulkBalanceModal.vue'

async function openModal() {
  const wrapper = mount(BulkBalanceModal, { props: { show: false, selectedIds: [1, 2, 3] } })
  await wrapper.setProps({ show: true })
  return wrapper
}

async function submit(wrapper: Awaited<ReturnType<typeof openModal>>) {
  await wrapper.get('form').trigger('submit')
  await flushPromises()
}

function sentKeys(): string[] {
  return mocks.batchUpdateBalance.mock.calls.map((call) => call[1] as string)
}

describe('BulkBalanceModal', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(window, 'confirm').mockReturnValue(true)
  })

  // Users are credited one by one on the server, so a request that timed out on
  // the client may already have run. Retrying it must not credit them twice.
  it('reuses the idempotency key when the same submission is retried', async () => {
    mocks.batchUpdateBalance
      .mockRejectedValueOnce({ status: 0, code: 'ECONNABORTED', message: 'timeout of 120000ms exceeded' })
      .mockResolvedValueOnce({ affected: 3, skipped: [] })
    const wrapper = await openModal()
    await wrapper.get('[data-test="operation-select"]').setValue('add')
    await wrapper.get('[data-test="amount-input"]').setValue('5')

    await submit(wrapper)
    await submit(wrapper)

    const [first, second] = sentKeys()
    expect(first).toMatch(/^user-batch-balance-/)
    expect(second).toBe(first)
  })

  it('treats a changed amount as a new submission', async () => {
    mocks.batchUpdateBalance.mockRejectedValue({ status: 400, message: 'bad request' })
    const wrapper = await openModal()
    await wrapper.get('[data-test="operation-select"]').setValue('add')
    await wrapper.get('[data-test="amount-input"]').setValue('5')
    await submit(wrapper)
    await wrapper.get('[data-test="amount-input"]').setValue('6')
    await submit(wrapper)

    const [first, second] = sentKeys()
    expect(second).not.toBe(first)
  })

  // apiClient rejects with { status, code, message } and no `response` field,
  // so reading error.response.data always fell back to the generic text.
  it('shows the server error message instead of the generic failure', async () => {
    mocks.batchUpdateBalance.mockRejectedValue({ status: 400, code: 'INVALID', message: 'balance would go negative for user 2' })
    const wrapper = await openModal()
    await wrapper.get('[data-test="amount-input"]').setValue('0')
    await submit(wrapper)

    expect(mocks.showError).toHaveBeenCalledWith('balance would go negative for user 2')
  })
})
