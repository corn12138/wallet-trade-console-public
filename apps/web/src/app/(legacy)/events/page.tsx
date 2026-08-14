import { redirect } from 'next/navigation';

/**
 * `/events` is retired. The old Tailwind global-events page was visually and
 * i18n-inconsistent with the Atlas shell; `/activity` is the Atlas-native,
 * fully-localized wallet event feed backed by the same real event endpoints.
 * To avoid shipping two inconsistent event products, `/events` permanently
 * redirects to `/activity`.
 */
export default function EventsPage() {
  redirect('/activity');
}
