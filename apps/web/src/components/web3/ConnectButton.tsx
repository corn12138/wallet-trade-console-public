import { useState } from 'react';
import {
  useAccount,
  useBalance,
  useConnect,
  useDisconnect,
  useChainId,
  useSwitchChain,
} from 'wagmi';
import { useClientMounted } from '@/hooks/web3';
import { useAuth } from '@/lib/web3';
import { AuthenticatedWalletMenu } from './AuthenticatedWalletMenu';
import { ConnectButtonPendingAuth } from './ConnectButtonPendingAuth';
import type { ConnectButtonVariant } from './connect-button.utils';
import { formatBalanceLabel, resolveWalletConnectionStatus } from './connect-button.utils';
import { WalletConnectorMenu } from './WalletConnectorMenu';

interface ConnectButtonProps {
  variant?: ConnectButtonVariant;
}

export function ConnectButton({ variant = 'default' }: ConnectButtonProps) {
  const mounted = useClientMounted();
  const [isOpen, setIsOpen] = useState(false);

  const { address, isConnected, connector } = useAccount();
  const { connectors, connect, isPending: isConnecting } = useConnect();
  const { disconnect } = useDisconnect();
  const { data: balance } = useBalance({ address });
  const chainId = useChainId();
  const { switchChain, isPending: isSwitching } = useSwitchChain();

  const {
    isAuthenticated,
    isLoading: isAuthLoading,
    error,
    errorCode,
    status,
    signIn,
    signOut,
  } = useAuth();
  const walletStatus = resolveWalletConnectionStatus({
    authStatus: status,
    isAuthenticated,
    isConnected,
    isConnecting,
    connectors,
    chainId,
  });

  if (!mounted) {
    return (
      <div className="h-10 w-32 animate-pulse rounded-lg bg-gray-200 dark:bg-gray-700" />
    );
  }

  if (!isConnected) {
    return (
      <WalletConnectorMenu
        isOpen={isOpen}
        isConnecting={isConnecting}
        connectors={connectors}
        status={walletStatus}
        variant={variant}
        onToggle={() => setIsOpen((open) => !open)}
        onClose={() => setIsOpen(false)}
        onConnect={(nextConnector) => {
          connect({ connector: nextConnector });
          setIsOpen(false);
        }}
      />
    );
  }

  if (!isAuthenticated && address) {
    return (
      <ConnectButtonPendingAuth
        address={address}
        isAuthLoading={isAuthLoading}
        error={error}
        errorCode={errorCode}
        status={walletStatus}
        variant={variant}
        onSignIn={signIn}
        onDisconnect={() => disconnect()}
      />
    );
  }

  if (!address) {
    return null;
  }

  return (
    <AuthenticatedWalletMenu
      isOpen={isOpen}
      address={address}
      connectorName={connector?.name}
      balanceLabel={formatBalanceLabel(balance)}
      chainId={chainId}
      status={walletStatus}
      isSwitching={isSwitching}
      variant={variant}
      onToggle={() => setIsOpen((open) => !open)}
      onClose={() => setIsOpen(false)}
      onCopyAddress={() => navigator.clipboard.writeText(address)}
      onSwitchChain={(nextChainId) => switchChain({ chainId: nextChainId })}
      onSignOut={() => {
        signOut();
        setIsOpen(false);
      }}
    />
  );
}
