// Thin API client. Same-origin in production (served by Go); proxied in dev.
async function req(path, opts = {}) {
  const r = await fetch(path, { credentials: 'same-origin', ...opts });
  const data = await r.json().catch(() => ({}));
  if (!r.ok) {
    // Session expired or was revoked mid-use: reload to land on the login view.
    if (r.status === 401 && !path.startsWith('/api/auth/')) {
      window.location.reload();
    }
    throw new Error(data.error || `HTTP ${r.status}`);
  }
  return data;
}

const jsonPost = (path, body) =>
  req(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body)
  });

const b64url = (s) => btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');

export const api = {
  // Timeline cursor positioned at the top of the undated tail (the events with
  // no date come after every dated one, ordered by id desc). Cursors are
  // "U|<id>" base64url-encoded (app.Cursor.Encode); the all-f id sorts above
  // every real one, so this admits every undated event. Used to list the
  // members of an undated cluster, which no from/to bound can reach.
  undatedCursor: b64url('U|ffffffff-ffff-ffff-ffff-ffffffffffff'),
  me: () => req('/api/auth/me'),
  // Identity providers to log in through: [{id, name}]; the login starts with a
  // full navigation to /api/auth/oidc/<id>/start.
  oidcProviders: () => req('/api/auth/oidc'),
  // What the login page offers: {signup: bool, providers: [{id, name}]}.
  authOptions: () => req('/api/auth/options'),
  // Starts linking provider `id` to the signed-in account: answers {url}, the
  // provider page to navigate to; it returns to /account?linked=<id> or
  // /account?link_error=<code>.
  linkProvider: (id) =>
    req('/api/auth/oidc/' + encodeURIComponent(id) + '/link', { method: 'POST' }),
  signup: (email, password) => jsonPost('/api/auth/signup', { email, password }),
  login: (email, password) => jsonPost('/api/auth/login', { email, password }),
  logout: () => req('/api/auth/logout', { method: 'POST' }),
  changePassword: (current, newPassword) =>
    jsonPost('/api/auth/password', { current, new: newPassword }),
  deleteAccount: (password) => jsonPost('/api/auth/delete', { password }),
  sources: () => req('/api/sources'),
  events: (qs) => req('/api/events?' + qs),
  // Collapsed timeline: runs of repetitive events arrive as one {kind:"cluster"}
  // item each; `limit` counts items and the page is `items`, not `events`.
  eventsCollapsed: (qs) => req('/api/events?collapse=1&' + qs),
  histogram: (qs) => req('/api/events/histogram?' + qs),
  // by = 'source' (buckets carry by_source) or 'category' (by_category).
  histogramBy: (by, qs) =>
    req('/api/events/histogram?by=' + encodeURIComponent(by) + (qs ? '&' + qs : '')),
  // Uploads every file as one part of a single import. XHR rather than fetch:
  // it reports upload progress, which matters for multi-GB archives.
  importZips: (source, label, files, onProgress) =>
    new Promise((resolve, reject) => {
      const fd = new FormData();
      for (const f of files) fd.append('archive', f);
      const q =
        'source=' +
        encodeURIComponent(source) +
        '&account=' +
        encodeURIComponent(label || 'default');
      const xhr = new XMLHttpRequest();
      xhr.open('POST', '/api/import?' + q);
      xhr.upload.onprogress = (e) => {
        if (onProgress && e.lengthComputable) onProgress(e.loaded, e.total);
      };
      xhr.onerror = () => reject(new Error('upload failed — connection lost'));
      xhr.onload = () => {
        let data = {};
        try {
          data = JSON.parse(xhr.responseText);
        } catch {
          /* non-JSON error body */
        }
        if (xhr.status >= 200 && xhr.status < 300) return resolve(data);
        if (xhr.status === 401) window.location.reload();
        reject(new Error(data.error || `HTTP ${xhr.status}`));
      };
      xhr.send(fd);
    }),
  importStatus: (id) => req('/api/imports/' + id),
  imports: () => req('/api/imports'),
  reprocessImport: (id) => req('/api/imports/' + id + '/reprocess', { method: 'POST' }),
  // Cancels a queued import at once; a running one stops within seconds.
  cancelImport: (id) => req('/api/imports/' + id + '/cancel', { method: 'POST' }),
  deleteImport: (id) => req('/api/imports/' + id, { method: 'DELETE' })
};
