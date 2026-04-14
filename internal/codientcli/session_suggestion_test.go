package codientcli

import (
	"testing"

	"codient/internal/agent"
)

func TestLooksLikeSuggestionList(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		s    string
		want bool
	}{
		{
			name: "three plain bullets",
			s:    "- one\n- two\n- three\n",
			want: true,
		},
		{
			name: "numbered list",
			s:    "1. first\n2. second\n3. third\n",
			want: true,
		},
		{
			name: "numbered markdown headings",
			s:    "## 1. First\n## 2. Second\n## 3. Third\n",
			want: true,
		},
		{
			name: "web search link bullets",
			s:    "- [Go 1.26 notes](https://go.dev/doc/go1.26)\n- [Release](https://example.com)\n- [Blog](https://example.com)\n",
			want: false,
		},
		{
			name: "generic section headers only",
			s:    "## Official documentation\n## Community\n## Related\n",
			want: false,
		},
		{
			name: "two bullets only",
			s:    "- a\n- b\n",
			want: false,
		},
		{
			name: "substantive bullets not links",
			s:    "- Consider using context cancellation\n- Add structured logging\n- Retry transient errors\n",
			want: true,
		},
		{
			name: "checklist status bullets from search summaries",
			s:    "- ✅ Uses actions/setup-go\n- ✅ Runs make check\n- ✅ Tests pass\n",
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := looksLikeSuggestionList(tt.s); got != tt.want {
				t.Fatalf("looksLikeSuggestionList() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParsePostReplyGateAnswer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want bool
	}{
		{"YES", true},
		{"yes", true},
		{"YES — external summary only", true},
		{"NO", false},
		{"no.", false},
		{"The answer is YES", false},
		{"", false},
		{"MAYBE", false},
	}
	for _, tt := range tests {
		if got := parsePostReplyGateAnswer(tt.in); got != tt.want {
			t.Errorf("parsePostReplyGateAnswer(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestSkipSuggestionVerifyForResearchTurn(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		info agent.PostReplyCheckInfo
		skip bool
	}{
		{
			name: "web_search only, generic user prompt",
			info: agent.PostReplyCheckInfo{
				User:      "please perform a test web search",
				TurnTools: []string{"web_search"},
			},
			skip: true,
		},
		{
			name: "web_search + user wants repo suggestions",
			info: agent.PostReplyCheckInfo{
				User:      "search the web for Go 1.26 and suggest what we should change in our repo",
				TurnTools: []string{"web_search"},
			},
			skip: false,
		},
		{
			name: "web_search and mutating tool",
			info: agent.PostReplyCheckInfo{
				User:      "look up X then write a file",
				TurnTools: []string{"web_search", "write_file"},
			},
			skip: false,
		},
		{
			name: "no web_search",
			info: agent.PostReplyCheckInfo{
				User:      "read three files",
				TurnTools: []string{"read_file", "read_file", "read_file"},
			},
			skip: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := skipSuggestionVerifyForResearchTurn(tt.info); got != tt.skip {
				t.Fatalf("skipSuggestionVerifyForResearchTurn() = %v, want %v", got, tt.skip)
			}
		})
	}
}
