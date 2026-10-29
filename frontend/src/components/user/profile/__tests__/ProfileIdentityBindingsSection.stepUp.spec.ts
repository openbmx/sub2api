import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { setActivePinia, createPinia } from 'pinia'

const mocks = vi.hoisted(() => ({
  startOAuthBinding: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ fullPath: '/profile', query: {} }),
}))

vi.mock('@/api/user', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/user')>()
  return { ...actual, startOAuthBinding: mocks.startOAuthBinding }
})

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

vi.mock('@/components/auth/TotpStepUpDialog.vue', () => ({
  default: {
    name: 'TotpStepUpDialog',
    props: ['controller'],
    template: '<div data-test="step-up-dialog" />',
  },
}))

import ProfileIdentityBindingsSection from '../ProfileIdentityBindingsSection.vue'
import { useAppStore } from '@/stores'

const stepUpRequired = { status: 403, code: 'STEP_UP_REQUIRED', message: 'This operation requires recent two-factor verification' }

function mountSection() {
  return mount(ProfileIdentityBindingsSection, {
    props: {
      user: {
        id: 1,
        email: 'admin@example.com',
        role: 'admin',
        auth_bindings: { linuxdo: { bound: false } },
      } as never,
      linuxdoEnabled: true,
    },
    global: { stubs: { Icon: true } },
  })
}

describe('ProfileIdentityBindingsSection step-up', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  // Binding an OAuth login to an admin account needs step-up. The binding start
  // used to be fire-and-forget, so STEP_UP_REQUIRED was an unhandled rejection
  // and the button did nothing.
  it('prompts for a TOTP code and retries when binding needs step-up', async () => {
    mocks.startOAuthBinding.mockRejectedValueOnce(stepUpRequired).mockResolvedValueOnce(undefined)
    const wrapper = mountSection()
    const dialog = wrapper.findComponent({ name: 'TotpStepUpDialog' })
    expect(dialog.exists()).toBe(true)

    await wrapper.get('[data-testid="profile-binding-linuxdo-action"]').trigger('click')
    await flushPromises()
    const controller = dialog.props('controller') as { visible: { value: boolean }; onVerified: () => void }
    expect(controller.visible.value).toBe(true)

    controller.onVerified()
    await flushPromises()
    expect(mocks.startOAuthBinding).toHaveBeenCalledTimes(2)
  })

  it('reports a failure instead of swallowing it', async () => {
    mocks.startOAuthBinding.mockRejectedValueOnce({ status: 500, message: 'binding unavailable' })
    const appStore = useAppStore()
    const showError = vi.spyOn(appStore, 'showError')
    const wrapper = mountSection()

    await wrapper.get('[data-testid="profile-binding-linuxdo-action"]').trigger('click')
    await flushPromises()
    expect(showError).toHaveBeenCalledWith('binding unavailable')
  })
})
