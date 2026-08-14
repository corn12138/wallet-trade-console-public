package mobilebff

import (
	"regexp"
	"strings"
)

// Fixtures ported from legacy NestJS mobile-bff/mobile-bff.fixtures.ts.

const (
	defaultArticleImageURL   = "https://lh3.googleusercontent.com/aida-public/AB6AXuCt-adYCK9Jw5YYZA5jgFHN1GG4ACXV3occYfwQBDsglDR2SxIKTKLt3Z2gQjZEy9m2_okIIyC3ozeE9-zEHpvs9eLzFBCDFaRFUlBaM5Y6Onbv2wCzUmqYzTK_RpxH2oospP3-UvywTZKhaO4jY0esKqtapuwgS0vTpfLFp2y96LMc1rEA8m3bPnq-1ja5p52g2v5G3GYERIKEilHIw5KzdlA3P9l8KBT_x_Si0fW-WQSXFveca8ni0hliWFrO_AmawtTrQKbNFnc"
	defaultAvatarURL         = "https://lh3.googleusercontent.com/aida-public/AB6AXuBjQYtJlXB0rx6I8P1z5znNvCahiZMZT8FW_QoojNuRNJslOgj2p-CWhExUABZBco6NHraR9nUrkAww6m0BWpOLaDjHnaGiAcMFQXdaj2EFlYORdtJXWlnwb7tzP25h9pESryQoPBlQ1nf8jIIcRpXt1FE0GOThyrHD3Y50irw0NzmHcI2JLMRekC0MLtWbE2K00HIUu2QCOCP1uZNx8Puo5-Z7caFXW8EATYckpIQFzBC_qm65zKZx9Lefd4hbvDJhQopZAw-N0wA"
	defaultProfileAvatarURL  = "https://lh3.googleusercontent.com/aida-public/AB6AXuDyVQG4mUCLlpSfVlT26ckQoWao5xSbw3sJa1VYnftyvcS3RYAKkA4zyKKSKH9XCAGanUHISjzIvKOZfKom8tEf15q9YsS8V5dx40zGyLwcsfZDqm-K3gmtGh-AF6E4bcyGQCdXRjmkUoY5khIGeh0-TPEwq4gfQIbGRd-vzvmK-2W4SNgBDa3L8OJu-ZuxzANOqOQ-8EiuT7_zlX0rEgQIs98uDsoZ9H0rqqyPoXjM5pGtr0HXBiOvO9ffFPBA4Ni8zmjDFTMI7I8"
	defaultTopicHeroImageURL = "https://lh3.googleusercontent.com/aida-public/AB6AXuB4dOp-dx2_K_P0iujWJ26cDlr7glEvxWaTKz0UlL6S3WSWvUENhYgjM_fVlB0EzDLPTVrjMpvNL1PWbrVAXF-v9_tKfjyeqil2FiiQUUIMw8plT5FxBOf1AtjQ2Kp5G_xqRzYiEu7xNx4N7ptFgOsc_bVXwWcn9D-qin4A67veaYJ5k77n4YowARlj3ikpneXku8OWX-t9AC0isANO65mhP_mTC9FiDyIl7SMmno8StD_7vkIimCMYWQWvZgR6VEV94SqAcwZh6N8"
	defaultTopicSlug         = "scholar-path"
)

var defaultSearchQuickAccess = []string{"Ink Painting", "Poetry Metres", "Ceramic Glazing", "Zen Gardens"}

var defaultSearchHistory = []string{"Calligraphy brush care", "History of Tea Ceremonies", "Scholar's Garden Layout"}

var defaultSyllabus = []string{"Four Treasures fundamentals", "Negative space and Ma", "Reading room rituals"}

// defaultVideoResults mirrors DEFAULT_VIDEO_RESULTS (StaticCardInput cards).
func defaultVideoResults() []Card {
	return []Card{
		{
			ID:          "alchemy-ink",
			Title:       "Ink & Algorithms",
			Subtitle:    "Video Insight",
			Description: "A short visual essay on generative art's debt to classical composition.",
			ImageURL:    "https://lh3.googleusercontent.com/aida-public/AB6AXuArYfdDPExUSdWDbDZfi1O5o3PkbWZNHymaY5vqunqj8MrKtPglxagApb9F-SzFX7OLFF5Ep0YELTB2dRy1ny4zn1jQQklynjtuh80jueSqWIdVwhx9yUSIZyRrMcAk91v1pfLlULkmhz4XamRKnlvILpTUtXakWUZpOEjZiOmUORkKvzUfpWQ-zI_vl50z0bW0T84ehMCC0YxMUmgMUUYBNnCSrXSIQplflez9fC0f794vvi2uetcMMZ8a4hclnFeRVF29dTHU5FU",
			AppURL:      "app://video/feed?id=alchemy-ink",
			RuntimeHint: "native_media",
			Tags:        []string{"#GenerativeArt", "#VisualEssay"},
		},
		{
			ID:          "reference-scroll",
			Title:       "Reference Scroll",
			Subtitle:    "Direct Message",
			Description: "Annotated excerpts and field notes prepared for rapid scholarly review.",
			ImageURL:    "https://lh3.googleusercontent.com/aida-public/AB6AXuCSYWhecNT-82201sbSepLXrMeaEtjSb1WtuVDcmqb7oEdHYn4DKAwe_Nthc7Cv-GNM-5tkZ4HWnNtlx2q_FSgjtUwNNbmtsjBN4m-wJLFWqeFNxkggXT1RNkfHVIygehT2H4KPKwyWWpMEtlKtkPLge1Dn6wpZ2dxfDpe2h118IBLKK1OlhGnRTgtTxsBfs24P2tIQvBBk8QpfnyNMbF7koC5BMRK1sCbuFDIhH17JRa0DgCmTOqAthMXvKjFgPs5rJt3JW3Y8wKQ",
			AppURL:      "app://video/feed?id=reference-scroll",
			RuntimeHint: "native_media",
			Tags:        []string{"#Archive", "#Reference"},
		},
	}
}

// defaultSearchTopicCards mirrors DEFAULT_SEARCH_TOPIC_CARDS.
func defaultSearchTopicCards() []Card {
	return []Card{
		{
			ID:          "topic-mastering-four-treasures",
			Title:       "Mastering the Four Treasures",
			Subtitle:    "1.2k Scholars following",
			Description: "Brush, ink, paper, and stone as a complete scholarly workflow.",
			AppURL:      "app://topic/landing?slug=scholar-path",
			RuntimeHint: "web",
		},
		{
			ID:          "topic-neo-confucianism-digital-spaces",
			Title:       "Neo-Confucianism in Digital Spaces",
			Subtitle:    "856 Scholars following",
			Description: "How classical study habits survive inside modern digital systems.",
			AppURL:      "app://topic/landing?slug=scholar-path",
			RuntimeHint: "web",
		},
		{
			ID:          "topic-silent-meditation",
			Title:       "The Art of Silent Meditation",
			Subtitle:    "2.4k Scholars following",
			Description: "A guided path for focus rituals, pace, and contemplative reading.",
			AppURL:      "app://topic/landing?slug=scholar-path",
			RuntimeHint: "web",
		},
	}
}

var slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)
var slugTrimDash = regexp.MustCompile(`^-+|-+$`)

// normalizeSlug mirrors the module-level normalizeSlug().
func normalizeSlug(value string) string {
	s := strings.ToLower(strings.TrimSpace(value))
	s = slugNonAlnum.ReplaceAllString(s, "-")
	s = slugTrimDash.ReplaceAllString(s, "")
	return s
}

// humanizeSlug mirrors the module-level humanizeSlug().
func humanizeSlug(slug string) string {
	parts := strings.Split(slug, "-")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		out = append(out, strings.ToUpper(p[:1])+p[1:])
	}
	return strings.Join(out, " ")
}
