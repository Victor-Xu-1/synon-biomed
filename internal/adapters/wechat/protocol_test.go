package wechat

import "testing"

func TestBuildClientVersion(t *testing.T) {
	if got := BuildClientVersion("2.1.7"); got != (2<<16)|(1<<8)|7 {
		t.Fatalf("BuildClientVersion(2.1.7) = %d", got)
	}
	if got := BuildClientVersion("1.0.11"); got != 65547 {
		t.Fatalf("BuildClientVersion(1.0.11) = %d", got)
	}
}

func TestExtractWechatText(t *testing.T) {
	plain := ExtractText([]MessageItem{{Type: 1, TextItem: &TextItem{Text: "hello"}}})
	if plain != "hello" {
		t.Fatalf("plain text = %q", plain)
	}

	ignored := ExtractText([]MessageItem{{Type: 3}})
	if ignored != "" {
		t.Fatalf("unsupported message type should be ignored: %q", ignored)
	}

	quoted := ExtractText([]MessageItem{{
		Type:     1,
		TextItem: &TextItem{Text: "reply"},
		RefMsg: &RefMessage{
			Title:       "quote title",
			MessageItem: &MessageItem{Type: 1, TextItem: &TextItem{Text: "quoted body"}},
		},
	}})
	want := "[引用: quote title | quoted body]\nreply"
	if quoted != want {
		t.Fatalf("quoted text = %q, want %q", quoted, want)
	}
}

func TestCollectWechatMediaCandidates(t *testing.T) {
	candidates := CollectMediaCandidates([]MessageItem{
		{
			Type:  2,
			MsgID: "img-1",
			ImageItem: &ImageItem{
				AESKey: "00112233445566778899aabbccddeeff",
				Media:  &CDNMedia{FullURL: "https://cdn.example.com/image", EncryptQueryParam: "enc=1"},
			},
		},
		{
			Type:  4,
			MsgID: "file-1",
			FileItem: &FileItem{
				FileName: "report.pdf",
				Media:    &CDNMedia{FullURL: "https://cdn.example.com/file"},
			},
		},
		{
			Type:      5,
			MsgID:     "video-1",
			VideoItem: &VideoItem{Media: &CDNMedia{FullURL: "https://cdn.example.com/video.mp4"}},
		},
	})

	if len(candidates) != 3 {
		t.Fatalf("len(candidates) = %d, want 3", len(candidates))
	}
	if candidates[0].Kind != "image" || candidates[0].Name != "wechat-image-img-1.jpg" {
		t.Fatalf("image candidate = %+v", candidates[0])
	}
	if candidates[1].Kind != "file" || candidates[1].Name != "report.pdf" || candidates[1].MimeType != "application/pdf" {
		t.Fatalf("file candidate = %+v", candidates[1])
	}
	if candidates[2].Kind != "file" || candidates[2].Name != "wechat-video-video-1.mp4" || candidates[2].MimeType != "video/mp4" {
		t.Fatalf("video candidate = %+v", candidates[2])
	}
}
