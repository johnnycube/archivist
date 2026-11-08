package meta

import "testing"

func TestThreadHelpers(t *testing.T) {
	for member, want := range map[string]string{
		"your_instagram_activity/messages/inbox/janedoe_1234/message_1.json":    "inbox/janedoe_1234",
		"your_facebook_activity/messages/message_requests/max_5/message_2.json": "message_requests/max_5",
		"messages/archived_threads/messages/inbox/nested_9/message_1.json":      "inbox/nested_9",
		"no_such_dir/janedoe_1234/message_1.json":                               "no_such_dir/janedoe_1234",
	} {
		if got := ThreadOf(member); got != want {
			t.Errorf("ThreadOf(%q) = %q, want %q", member, got, want)
		}
	}
	if got := ThreadLabel([]string{"A", "B", "C", "D", "E"}); got != "A, B, C +2 more" {
		t.Errorf("ThreadLabel = %q", got)
	}
	ex := MessageExtractor{}
	if !ex.Match("x/messages/inbox/t/message_1.json") || ex.Match("x/messages/inbox/t/photos/1.jpg") || ex.Match("x/inbox/t/message_1.json") {
		t.Error("MessageExtractor.Match claims the wrong members")
	}
}

func TestRecMixesKindIntoHash(t *testing.T) {
	a, b := Rec("follow", []byte(`{}`)), Rec("follower", []byte(`{}`))
	if a.ContentHash == b.ContentHash || a.ContentHash != Rec("follow", []byte(`{}`)).ContentHash {
		t.Errorf("hashes: %s %s", a.ContentHash, b.ContentHash)
	}
}

func TestIsAttachment(t *testing.T) {
	for member, want := range map[string]bool{
		"your_facebook_activity/messages/inbox/ann_1/files/contract.pdf": true,
		"your_instagram_activity/messages/inbox/ann_1/audio/clip.xyz":    true,
		"messages/inbox/ann_1/photos/1.jpg":                              true,
		"messages/inbox/ann_1/message_1.json":                            false,
		"messages/inbox/ann_1/files/index.json":                          false,
		"messages/inbox/ann_1/notes.txt":                                 false, // not in an attachment directory
		"your_facebook_activity/posts/media/Album_1/scan.tiff":           true,
		"media/posts/202401/clip.srt":                                    true,
		"media/stories/202401/story.dng":                                 true,
		"your_facebook_activity/stories/archived_stories.json":           false,
		// Instagram: every media/<kind>/ folder is referenced by a data file
		// (reels.json, archived_posts.json, other_content.json, profile_photos.json).
		"media/reels/202401/reel.mp4":                     true,
		"media/archived_posts/202401/old.heic":            true,
		"media/other/file.bin":                            true,
		"media/profile/202401/pic.jpg":                    true,
		"messages/inbox/ann_1/stickers_used/sticker.webp": true,
		"your_facebook_activity/posts/album/0.json":       false,
		"files/readme.pdf":                                false, // files/ outside messages/
	} {
		if got := IsAttachment(member); got != want {
			t.Errorf("IsAttachment(%q) = %v, want %v", member, got, want)
		}
	}
}
