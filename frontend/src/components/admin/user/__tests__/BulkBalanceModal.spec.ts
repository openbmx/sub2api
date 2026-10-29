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

import BulkBalanceModal, { resetPendingBulkBalanceSubmission } from '../BulkBalanceModal.vue'

async function openModal(selectedIds: number[] = [1, 2, 3]) {
  const wrapper = mount(BulkBalanceModal, { props: { show: false, selectedIds } })
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
    resetPendingBulkBalanceSubmission()
  })

  // The server keeps running a batch after the browser gives up. Closing and
  // reopening the dialog used to mint a fresh key, so resubmitting the same batch
  // credited every user the first run had finished a second time.
  it('keeps the key across closing and reopening while the batch is unanswered', async () => {
    mocks.batchUpdateBalance
      .mockRejectedValueOnce({ status: 0, code: 'ECONNABORTED', message: 'timeout of 120000ms exceeded' })
      .mockResolvedValueOnce({ affected: 3, skipped: [] })
    const wrapper = await openModal()
    await wrapper.get('[data-test="operation-select"]').setValue('add')
    await wrapper.get('[data-test="amount-input"]').setValue('5')
    await submit(wrapper)

    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await wrapper.get('[data-test="operation-select"]').setValue('add')
    await wrapper.get('[data-test="amount-input"]').setValue('5')
    await submit(wrapper)

    const [first, second] = sentKeys()
    expect(second).toBe(first)
  })

  // The server hashes the request body. Sending the ids in selection order made
  // the same batch picked in another order reuse its key with a different hash,
  // which the server answers with 409 on every retry.
  it('sends one canonical id order so a re-picked batch keeps its key', async () => {
    mocks.batchUpdateBalance
      .mockRejectedValueOnce({ status: 0, code: 'ECONNABORTED', message: 'timeout of 120000ms exceeded' })
      .mockResolvedValueOnce({ affected: 3, skipped: [] })
    const wrapper = await openModal([3, 1, 2])
    await wrapper.get('[data-test="operation-select"]').setValue('add')
    await wrapper.get('[data-test="amount-input"]').setValue('5')
    await submit(wrapper)
    await wrapper.setProps({ selectedIds: [2, 3, 1, 1] })
    await submit(wrapper)

    const [first, second] = sentKeys()
    expect(second).toBe(first)
    const sentIds = mocks.batchUpdateBalance.mock.calls.map((call) => call[0].user_ids)
    expect(sentIds).toEqual([
      [1, 2, 3],
      [1, 2, 3],
    ])
  })

  // One pending slot meant trying batch B in between cost batch A its key, so
  // going back to A ran it a second time.
  it('keeps each unanswered batch on its own key', async () => {
    const timeout = { status: 0, code: 'ECONNABORTED', message: 'timeout of 120000ms exceeded' }
    mocks.batchUpdateBalance
      .mockRejectedValueOnce(timeout)
      .mockRejectedValueOnce(timeout)
      .mockResolvedValueOnce({ affected: 3, skipped: [] })
    const wrapper = await openModal()
    await wrapper.get('[data-test="operation-select"]').setValue('add')
    await wrapper.get('[data-test="amount-input"]').setValue('5')
    await submit(wrapper)
    await wrapper.get('[data-test="amount-input"]').setValue('6')
    await submit(wrapper)
    await wrapper.get('[data-test="amount-input"]').setValue('5')
    await submit(wrapper)

    const [batchA, batchB, batchAAgain] = sentKeys()
    expect(batchB).not.toBe(batchA)
    expect(batchAAgain).toBe(batchA)
  })

  it('cannot be closed or cancelled while applying', async () => {
    mocks.batchUpdateBalance.mockReturnValue(new Promise(() => {}))
    const wrapper = await openModal()
    await wrapper.get('[data-test="amount-input"]').setValue('5')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(wrapper.get('[data-test="cancel"]').attributes('disabled')).toBeDefined()
    wrapper.findComponent({ name: 'BaseDialog' }).vm.$emit('close')
    expect(wrapper.emitted('close')).toBeUndefined()
  })

  it('hands skipped users back for re-selection', async () => {
    mocks.batchUpdateBalance.mockResolvedValueOnce({
      affected: 2,
      success_ids: [1, 3],
      skipped: [{ id: 2, reason: 'balance cannot be negative' }],
    })
    const wrapper = await openModal()
    await wrapper.get('[data-test="operation-select"]').setValue('subtract')
    await wrapper.get('[data-test="amount-input"]').setValue('5')
    await submit(wrapper)

    expect(wrapper.emitted('success')?.[0]).toEqual([2, [2]])
    expect(mocks.showError).toHaveBeenCalledWith('admin.users.bulkBalance.partialSuccessReselected')
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
