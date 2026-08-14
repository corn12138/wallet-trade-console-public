import { describe, expect, it } from 'vitest';
import { getSiwePromptKey, shouldAutoOpenSiweModal } from './siwe-modal-state';

describe('siwe modal state', () => {
  it('builds a stable prompt key from wallet address and chain', () => {
    expect(getSiwePromptKey('0xABCdef0000000000000000000000000000000001', 11155111)).toBe(
      '0xabcdef0000000000000000000000000000000001:11155111',
    );
  });

  it('auto-opens SIWE for a connected unauthenticated wallet', () => {
    expect(shouldAutoOpenSiweModal({
      isConnected: true,
      isAuthenticated: false,
      isLoading: false,
      walletState: 'siwe',
      modal: null,
      promptKey: '0x1:11155111',
      dismissedPromptKey: null,
    })).toBe(true);
  });

  it('does not immediately reopen after the user dismisses the same SIWE prompt', () => {
    expect(shouldAutoOpenSiweModal({
      isConnected: true,
      isAuthenticated: false,
      isLoading: false,
      walletState: 'siwe',
      modal: null,
      promptKey: '0x1:11155111',
      dismissedPromptKey: '0x1:11155111',
    })).toBe(false);
  });

  it('can auto-open again when the wallet or chain changes', () => {
    expect(shouldAutoOpenSiweModal({
      isConnected: true,
      isAuthenticated: false,
      isLoading: false,
      walletState: 'siwe',
      modal: null,
      promptKey: '0x2:11155111',
      dismissedPromptKey: '0x1:11155111',
    })).toBe(true);
  });
});
