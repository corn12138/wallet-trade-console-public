export type LaunchType = 'newcoin' | 'ido' | 'burning';

export interface TokenForm {
  symbol: string;
  name: string;
  description: string;
  image: File | null;
  imagePreview: string;
  banner: File | null;
  bannerPreview: string;
  tags: string[];
  twitter: string;
  discord: string;
  telegram: string;
  website: string;
  whitepaper: string;
  preBuyPercent: number;
  hasMargin: boolean;
  hasReservation: boolean;
  isOfficial: boolean;
  customAddress: string;
  useCustomAddress: boolean;
}

export interface SubmissionNotice {
  tone: 'idle' | 'info' | 'success' | 'error';
  message: string;
}

export const INITIAL_TOKEN_FORM: TokenForm = {
  symbol: '',
  name: '',
  description: '',
  image: null,
  imagePreview: '',
  banner: null,
  bannerPreview: '',
  tags: [],
  twitter: '',
  discord: '',
  telegram: '',
  website: '',
  whitepaper: '',
  preBuyPercent: 0,
  hasMargin: false,
  hasReservation: false,
  isOfficial: false,
  customAddress: '',
  useCustomAddress: false,
};
