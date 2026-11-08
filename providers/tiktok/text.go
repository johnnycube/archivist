package tiktok

import (
	"bufio"
	"encoding/json"
	"io"
	"path"
	"strings"

	"github.com/johnnycube/archivist/internal/provider"
)

// textEx reads the TXT flavour of the export: one file per section
// ("Activity/Like List.txt", "Your Activity/Watch History.txt",
// "Direct Messages/Direct Messages.txt", "Video/Videos.txt", …). Each record is
// a block of "Key: Value" lines separated by a blank line; the posts file uses
// "Video Link:" where the JSON has "Link". Direct messages are grouped under a
// "Chat History with <user>:" heading line.
//
// Public evidence for this layout is thin (the folder/file names and the
// "Link:" / "Video Link:" lines are documented by community tools; the exact
// block separators and the DM heading are inferred), so the parser is
// tolerant: keys are compared with spaces removed and case ignored, a line
// without a "Key: " prefix continues the previous value, and files whose name
// is not a known section stay with the importer's generic capture.
type textEx struct{}

// textSections maps a TXT file's base name (lower-case, no extension) to the
// record kind its blocks hold.
var textSections = map[string]string{
	"watch history":          "watch",
	"video browsing history": "watch",
	"browsing history":       "watch",
	"like list":              "like",
	"likes":                  "like",
	"favorite videos":        "save",
	"favorite effects":       "save",
	"favorite hashtags":      "save",
	"favorite sounds":        "save",
	"favorite songs":         "save",
	"search history":         "search",
	"searches":               "search",
	"login history":          "login",
	"following list":         "follow",
	"following":              "follow",
	"follower list":          "follower",
	"followers":              "follower",
	"share history":          "share",
	"comments":               "comment",
	"direct messages":        "message",
	"direct message":         "message",
	"videos":                 "post",
	"posts":                  "post",
	"post":                   "post",
	"block list":             "block",
}

func textSection(member string) (string, bool) {
	b := strings.ToLower(path.Base(member))
	if !strings.HasSuffix(b, ".txt") {
		return "", false
	}
	kind, ok := textSections[strings.TrimSpace(strings.TrimSuffix(b, ".txt"))]
	return kind, ok
}

func (textEx) Match(member string) bool {
	_, ok := textSection(member)
	return ok
}

const chatHeading = "chat history with"

func (textEx) Extract(member string, r io.Reader, emit func(provider.RawRecord) error) error {
	kind, _ := textSection(member)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)

	thread := ""
	var keys []string // insertion order, so the payload is deterministic
	fields := map[string]string{}
	last := ""
	flush := func() error {
		if len(keys) == 0 {
			return nil
		}
		obj := make(map[string]string, len(keys))
		for _, k := range keys {
			obj[k] = fields[k]
		}
		keys, fields, last = nil, map[string]string{}, ""
		payload, err := json.Marshal(obj)
		if err != nil {
			return err
		}
		if kind == "message" {
			payload, err = json.Marshal(dmRecord{Thread: thread, Message: payload})
			if err != nil {
				return err
			}
		}
		return emit(rec(kind, payload))
	}

	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		// "Chat History with jane:" (possibly prefixed with ">>>") opens a thread.
		if low := strings.ToLower(strings.TrimLeft(trimmed, "> ")); strings.HasPrefix(low, chatHeading) {
			if err := flush(); err != nil {
				return err
			}
			thread = chatPartner(strings.TrimLeft(trimmed, "> "))
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		key := strings.ReplaceAll(strings.TrimSpace(k), " ", "")
		if ok && key != "" && !strings.ContainsAny(key, "/ \t") && len(key) <= 40 && !strings.HasPrefix(strings.TrimSpace(line), "http") {
			if key == "VideoLink" {
				key = "Link"
			}
			if _, dup := fields[key]; dup && key == firstKey(keys) {
				// A repeated first key without a blank line: new record.
				if err := flush(); err != nil {
					return err
				}
			}
			if _, seen := fields[key]; !seen {
				keys = append(keys, key)
			}
			fields[key] = strings.TrimSpace(v)
			last = key
			continue
		}
		if last != "" {
			fields[last] += "\n" + line // continuation of a multi-line value
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return flush()
}

func firstKey(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}
