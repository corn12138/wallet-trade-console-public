'use client';
// `/nft` and `/nft-studio` both render the real NFT studio. The previous
// `_atlas/pages/NftStudioPage` re-export here was the mock that faked mint success
// (setTimeout, "NFT #0042", hardcoded contract). Point at the real page instead.
export { default } from '../nft-studio/page';
