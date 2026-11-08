// Event data comes from uploaded exports, i.e. it is untrusted: a crafted
// target_url of `javascript:…` would run in the app's origin when clicked.
// Only http(s) links are rendered as links; anything else is shown as text.
export function safeHref(url) {
  if (typeof url !== 'string') return '';
  try {
    const u = new URL(url.trim());
    return u.protocol === 'http:' || u.protocol === 'https:' ? u.href : '';
  } catch {
    return '';
  }
}
