import type { ModalState } from '../src/lib/ui/shell-contract';

// Compile-time negative fixtures keep generic props from reopening the shell
// boundary. An unused @ts-expect-error fails type-check if the contract loosens.
export function checkShellContract() {
  const chain = { kind: 'chain' } satisfies ModalState;
  const legacy = { kind: 'tx', props: { stage: 'pending', hash: '0x1234' } } satisfies ModalState;
  // @ts-expect-error wallet UI cannot receive execution parameters
  const walletPayload = { kind: 'wallet', props: { calldata: '0x1234' } } satisfies ModalState;
  // @ts-expect-error token pickers need a typed selection callback and token list
  const loosePicker = { kind: 'tokenPicker', props: {} } satisfies ModalState;
  // @ts-expect-error intent rendering needs a complete lifecycle and review contract
  const looseIntent = { kind: 'tx', props: { mode: 'intent' } } satisfies ModalState;
  // @ts-expect-error arbitrary dynamic components are not registered shell modals
  const remote = { kind: 'remoteModule', props: { url: 'https://example.invalid' } } satisfies ModalState;
  return [chain, legacy, walletPayload, loosePicker, looseIntent, remote];
}
