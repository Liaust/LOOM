package knowledge

import "testing"

func TestExtractMarkdownLinksFindsMarkdownAndWikilinks(t *testing.T) {
	text := "See [Project Plan](../plans/overview.md \"Overview\") and [[Daily Notes#Today|today]].\n![Alt](image.png)"

	links := ExtractMarkdownLinks(text)

	if len(links) != 2 {
		t.Fatalf("links len = %d, want 2: %#v", len(links), links)
	}
	if links[0].Kind != LinkKindMarkdown || links[0].RawTarget != "../plans/overview.md" || links[0].LinkText != "Project Plan" {
		t.Fatalf("markdown link = %#v, want parsed target/text", links[0])
	}
	if links[1].Kind != LinkKindWikilink || links[1].RawTarget != "Daily Notes#Today" || links[1].LinkText != "today" {
		t.Fatalf("wikilink = %#v, want target and alias", links[1])
	}
}

func TestNormalizeLinkTarget(t *testing.T) {
	got := normalizeLinkTarget(`<folder\Daily Note.md>`)
	if got != "folder/Daily Note.md" {
		t.Fatalf("normalizeLinkTarget = %q, want normalized slash target", got)
	}
}
