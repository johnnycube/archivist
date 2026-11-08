package rest

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/johnnycube/archivist/internal/app"
)

// inlineMediaTypes is the closed set of attachment types a browser may render
// in place, by lower-case file extension. It is a fixed map on purpose:
// mime.TypeByExtension reads the host's mime.types, so what it answers — and
// therefore what would be served inline — differs from machine to machine.
//
// Everything here is passive content: images, video, audio. Anything that can
// carry script or markup (svg, html, xml, pdf) is NOT, and never will be.
var inlineMediaTypes = map[string]string{
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".gif": "image/gif",
	".webp": "image/webp", ".avif": "image/avif", ".bmp": "image/bmp", ".heic": "image/heic",

	".mp4": "video/mp4", ".m4v": "video/mp4", ".webm": "video/webm", ".mov": "video/quicktime",

	".mp3": "audio/mpeg", ".m4a": "audio/mp4", ".ogg": "audio/ogg", ".oga": "audio/ogg",
	".opus": "audio/opus", ".wav": "audio/wav", ".aac": "audio/aac", ".amr": "audio/amr",
	".3gp": "audio/3gpp", ".3gpp": "audio/3gpp",
}

// mediaHeaders decides how the blob stored under key is served: one of the
// inline types above as itself, everything else as an opaque download.
func mediaHeaders(key string) (contentType, disposition string) {
	name := path.Base(key)
	if ct, ok := inlineMediaTypes[strings.ToLower(path.Ext(name))]; ok {
		return ct, "inline"
	}
	// FormatMediaType quotes or RFC 2231-encodes (filename*=utf-8''…) the name
	// as needed; it returns "" for a name it cannot represent.
	disposition = mime.FormatMediaType("attachment", map[string]string{"filename": name})
	if disposition == "" {
		disposition = "attachment"
	}
	return "application/octet-stream", disposition
}

// handleMedia streams a stored media attachment of the user's own event.
//
// The bytes come from an uploaded export, i.e. they are untrusted. They are
// safe to serve from the app's origin by construction: only the passive types
// of inlineMediaTypes are rendered at all, everything else is a download;
// nosniff (withCommonHeaders) keeps the browser from second-guessing the type;
// the sandbox CSP strips whatever is rendered of script and origin; and CORP
// keeps other sites from embedding a user's media.
func (s *Server) handleMedia(w http.ResponseWriter, r *http.Request) {
	uid := r.Context().Value(userKey).(string)
	idx, err := strconv.Atoi(r.PathValue("idx"))
	if err != nil {
		httpError(w, http.StatusBadRequest, "bad media index")
		return
	}
	rc, key, err := s.App.Timeline.Media(r.Context(), uid, r.PathValue("id"), idx)
	switch {
	case errors.Is(err, app.ErrNotFound), errors.Is(err, app.ErrNoMedia):
		httpError(w, http.StatusNotFound, "media not found")
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	defer rc.Close()

	h := w.Header()
	contentType, disposition := mediaHeaders(key)
	h.Set("Content-Type", contentType)
	h.Set("Content-Disposition", disposition)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	// Private photos and voice notes must not outlive logout, import deletion
	// or account deletion in a shared machine's browser cache.
	h.Set("Cache-Control", "private, no-store")
	// A blob store whose reader can seek gets Range support — 206, Accept-
	// Ranges, If-Range: what makes audio and video seekable in the browser
	// and what Safari needs to play video at all — from the standard library.
	// Both stores seek: the filesystem hands out an *os.File, the S3 store a
	// *minio.Object whose Seek+Read turns into ONE ranged GET (nothing is
	// fetched ahead of the client). Content-Type is set, so ServeContent does
	// not sniff; the empty name keeps it from guessing from an extension.
	// A store that only streams (none shipped) still gets the whole file.
	if rs, ok := rc.(io.ReadSeeker); ok {
		http.ServeContent(w, r, "", time.Time{}, rs)
		return
	}
	_, _ = io.Copy(w, rc)
}
