import type { ModalState, WalletState } from './AppContext';

export function getSiwePromptKey(address: string | undefined, chainId: number | undefined) {
  if (!address || !chainId) return null;
  return `${address.toLowerCase()}:${chainId}`;
}

export function shouldAutoOpenSiweModal(input: {
  isConnected: boolean;
  isAuthenticated: boolean;
  isLoading: boolean;
  walletState: WalletState;
  modal: ModalState;
  promptKey: string | null;
  dismissedPromptKey: string | null;
}) {
  if (!input.isConnected) return false;
  if (input.isAuthenticated || input.isLoading) return false;
  if (input.walletState !== 'siwe') return false;
  if (input.modal) return false;
  if (!input.promptKey) return false;
  return input.dismissedPromptKey !== input.promptKey;
}
