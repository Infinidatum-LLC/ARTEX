package notify

import (
	"encoding/json"
	"strings"
	"testing"
)

// sampleNotice is a task-completion-style notice used across the renderer tests.
func sampleNotice() Message {
	return Message{Notice: &Notice{
		Title: "Scan completed · Prod API sweep",
		Lines: []string{"Outcome: completed", "3 findings recorded.", "Critical 1 · High 2"},
		URL:   "https://artex.example.com/function/tasks/detail?id=42",
	}}
}

// TestNoticeTitleAndBodyAcrossRenderers checks that every channel renderer puts
// the notice title and body lines into its output, instead of falling through to
// the finding path (which would render "Vulnerability notification" with no items).
func TestNoticeTitleAndBodyAcrossRenderers(t *testing.T) {
	m := sampleNotice()

	if got := markdownTitle(m); got != m.Notice.Title {
		t.Fatalf("markdownTitle = %q, want the notice title %q", got, m.Notice.Title)
	}

	body, kept := markdownBody(m, 0)
	if kept != 1 {
		t.Fatalf("markdownBody kept = %d, want 1", kept)
	}
	for _, want := range []string{"Outcome: completed", "3 findings recorded.", "View on the platform"} {
		if !strings.Contains(body, want) {
			t.Errorf("markdown body missing %q:\n%s", want, body)
		}
	}

	html := htmlBody(m, 0)
	for _, want := range []string{"Scan completed", "Outcome: completed", m.Notice.URL} {
		if !strings.Contains(html, want) {
			t.Errorf("email html missing %q:\n%s", want, html)
		}
	}

	tg, keptTG := telegramHTML(m)
	if keptTG != 1 {
		t.Fatalf("telegramHTML kept = %d, want 1", keptTG)
	}
	for _, want := range []string{"<b>Scan completed", "Outcome: completed", m.Notice.URL} {
		if !strings.Contains(tg, want) {
			t.Errorf("telegram html missing %q:\n%s", want, tg)
		}
	}

	card, keptFe := feishuCard(m)
	if keptFe != 1 {
		t.Fatalf("feishuCard kept = %d, want 1", keptFe)
	}
	raw, _ := json.Marshal(card)
	for _, want := range []string{"Scan completed", "Outcome: completed", m.Notice.URL} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("feishu card missing %q:\n%s", want, string(raw))
		}
	}
}

// TestNoticeWebhookDefaultBodyIsValidJSON checks the webhook notice fallback
// renders valid JSON carrying the notice flag, lines, and URL — and crucially
// does not emit the finding items array (which would be empty and misleading).
func TestNoticeWebhookDefaultBodyIsValidJSON(t *testing.T) {
	m := sampleNotice()
	body, err := renderWebhookBody("", m)
	if err != nil {
		t.Fatalf("renderWebhookBody: %v", err)
	}
	var parsed struct {
		Title  string   `json:"title"`
		Notice bool     `json:"notice"`
		Lines  []string `json:"lines"`
		URL    string   `json:"url"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("webhook notice body is not valid JSON: %v\n%s", err, body)
	}
	if !parsed.Notice {
		t.Errorf("webhook notice body: notice flag = false, want true")
	}
	if parsed.Title != m.Notice.Title {
		t.Errorf("webhook notice title = %q, want %q", parsed.Title, m.Notice.Title)
	}
	if len(parsed.Lines) != len(m.Notice.Lines) {
		t.Errorf("webhook notice lines = %v, want %v", parsed.Lines, m.Notice.Lines)
	}
	if parsed.URL != m.Notice.URL {
		t.Errorf("webhook notice url = %q, want %q", parsed.URL, m.Notice.URL)
	}
	if strings.Contains(body, `"items"`) {
		t.Errorf("webhook notice body should not carry a findings items array:\n%s", body)
	}
}

// TestNoticeEscapesUntrustedBodyLines ensures notice body lines are escaped per
// channel, so a line that looks like markup cannot inject a link or tag. The
// notice body comes from the platform, but the task name embedded in it is
// operator-controlled text, so the same escaping discipline as findings applies.
func TestNoticeEscapesUntrustedBodyLines(t *testing.T) {
	m := Message{Notice: &Notice{
		Title: "Scan completed",
		Lines: []string{"name: [click](http://evil.tld)"},
	}}
	body, _ := markdownBody(m, 0)
	if strings.Contains(body, "](http://evil.tld)") {
		t.Errorf("markdown notice did not escape a link in a body line:\n%s", body)
	}
	tg, _ := telegramHTML(m)
	if strings.Contains(tg, "<a href") {
		t.Errorf("telegram notice must not emit a raw anchor from a body line:\n%s", tg)
	}
}
