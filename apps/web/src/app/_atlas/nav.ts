import type { IconName } from './Icon';

/* Nav structure: [path, icon, navKey]. Labels/subtitles resolve through the
   atlasShell.nav namespace so locale switching covers the whole sidebar.
   Bridge is no longer hidden: BridgeGateway is deployed, the relayer runs, and
   the page reads real chain state. Where a route cannot execute it says so
   with the specific reason (e.g. NO_DESTINATION_GATEWAY) rather than offering
   a control that would fail — which is why it is safe to show. */
export const GROUPS: { key: 'trade' | 'account' | 'build'; items: [string, IconName, string][] }[] = [
  {
    key: 'trade',
    items: [
      ['', 'home', 'home'],
      ['/trade', 'trade', 'trade'],
      ['/markets', 'markets', 'markets'],
      ['/swap', 'swap', 'swap'],
      ['/bridge', 'bridge', 'bridge'],
    ],
  },
  {
    key: 'account',
    items: [
      ['/portfolio', 'wallet', 'portfolio'],
      ['/earn', 'earn', 'earn'],
      ['/advanced-earn', 'staking', 'staking'],
      ['/activity', 'activity', 'activity'],
      ['/security', 'security', 'security'],
      ['/wallets', 'wallets', 'wallets'],
    ],
  },
  {
    key: 'build',
    items: [
      ['/create-token', 'rocket', 'createToken'],
      ['/nft-studio', 'nft', 'nftStudio'],
      ['/discover', 'discover', 'discover'],
      ['/ranking', 'trendUp', 'ranking'],
      ['/campaign', 'star', 'campaign'],
      ['/settings', 'settings', 'settings'],
    ],
  },
];

/** Flat list of [path, icon, navKey], in render order. */
export const NAV_ITEMS = GROUPS.flatMap((g) => g.items);
