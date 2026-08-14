import type { LaunchType } from './create-token.types';

export const availableTags = [
  'ai',
  'meme',
  'defi',
  'games',
  'nft',
  'metaverse',
  'social',
  'infrastructure',
] as const;

export const launchTypes: LaunchType[] = ['newcoin', 'ido', 'burning'];

export const toggleFields = [
  { key: 'hasMargin', label: 'addMargin' },
  { key: 'hasReservation', label: 'launchReservation' },
  { key: 'isOfficial', label: 'officialContact' },
] as const;
