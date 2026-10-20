import registry from './media-registry.generated';

export type TrustedMedia = Readonly<{ path: string; width: number; height: number; alt: string }>;
const SAME_ORIGIN_PNG = /^\/campaign-media\/[a-z0-9][a-z0-9-]{0,79}\.png$/;

/** Only an asset of the registry bundled with this build is rendered, from
 * this origin, with the registry's own size and alt text; campaign content
 * names an ID and can supply nothing else. */
export function trustedMedia(id: string | undefined, locale: string): TrustedMedia | null {
  if (id === undefined) return null;
  const asset = registry.assets.find((candidate) => candidate.id === id);
  if (!asset || asset.contentType !== 'image/png' || asset.path !== `/campaign-media/${asset.id}.png` || !SAME_ORIGIN_PNG.test(asset.path)) return null;
  return { path: asset.path, width: asset.width, height: asset.height, alt: asset.alt[locale === 'zh' ? 'zh' : 'en'] };
}
