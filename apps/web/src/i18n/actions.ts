'use server';

import { cookies } from 'next/headers';
import { redirect } from 'next/navigation';
import { getEnabledLocales } from './server/catalog';
import { sanitizeReturnPath } from './resolve';

/**
 * Server-backed language switch. The ONLY writer of the locale cookie:
 * validates the requested locale against the currently enabled set, sets the
 * cookie with server-controlled attributes, and redirects to a sanitized
 * same-origin path so the next render is a fresh SSR pass in the new locale.
 * JavaScript never writes this cookie.
 */
export async function setLocaleAction(formData: FormData): Promise<void> {
  const requested = String(formData.get('locale') ?? '').toLowerCase();
  const returnPath = sanitizeReturnPath(String(formData.get('returnPath') ?? '/'));

  const enabled = await getEnabledLocales();
  if (!enabled.some((l) => l.code === requested)) {
    // Unknown/disabled locale: no cookie write; re-render the current path.
    redirect(returnPath);
  }

  const cookieStore = await cookies();
  cookieStore.set('locale', requested, {
    path: '/',
    sameSite: 'lax',
    secure: process.env.NODE_ENV === 'production',
    httpOnly: true, // the browser renders what SSR selected; JS never reads it
    maxAge: 60 * 60 * 24 * 365,
  });
  redirect(returnPath);
}
