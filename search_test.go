package main

import (
	"testing"
)

func TestLooksLikeUUID(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"valid_lowercase", "550e8400-e29b-41d4-a716-446655440000", true},
		{"valid_uppercase", "550E8400-E29B-41D4-A716-446655440000", true},
		{"not_a_uuid", "not-a-uuid", false},
		{"empty_string", "", false},
		{"too_short", "550e8400-e29b-41d4-a716", false},
		{"too_long", "550e8400-e29b-41d4-a716-446655440000-extra", false},
		{"wrong_separator", "550e8400xe29b-41d4-a716-446655440000", false},
		{"invalid_hex", "gggggggg-gggg-gggg-gggg-gggggggggggg", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := looksLikeUUID(tt.input)
			if got != tt.want {
				t.Errorf("looksLikeUUID(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractMeetingID(t *testing.T) {
	uuid := "550e8400-e29b-41d4-a716-446655440000"

	tests := []struct {
		name string
		href string
		want string
	}{
		{
			name: "recording path",
			href: "/app/recording/" + uuid + "/overview",
			want: uuid,
		},
		{
			name: "share path via recording keyword",
			href: "/share/recording/" + uuid + "/highlights",
			want: uuid,
		},
		{
			name: "share without recording — no match",
			href: "/share/" + uuid,
			want: uuid, // falls through to last-segment UUID fallback
		},
		{
			name: "recordings list path",
			href: "/recordings/" + uuid,
			want: uuid,
		},
		{
			name: "full URL",
			href: "https://grain.com/app/recording/" + uuid + "/transcript",
			want: uuid,
		},
		{
			name: "uuid as last segment",
			href: "/some/other/path/" + uuid,
			want: uuid,
		},
		{
			name: "no uuid",
			href: "/app/settings/profile",
			want: "",
		},
		{
			name: "empty href",
			href: "",
			want: "",
		},
		{
			name: "relative path",
			href: "recording/" + uuid,
			want: uuid,
		},
		{
			name: "query params preserved",
			href: "/app/recording/" + uuid + "?tab=transcript",
			want: uuid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractMeetingID(tt.href)
			if got != tt.want {
				t.Errorf("extractMeetingID(%q) = %q, want %q", tt.href, got, tt.want)
			}
		})
	}
}

func TestExtractMeetingID_Injection(t *testing.T) {
	// SEC-1 audit: ensure URL-encoded/malicious hrefs don't break parsing.
	tests := []struct {
		name string
		href string
		want string // empty = correctly rejected
	}{
		{
			name: "encoded path",
			href: "/app/recording/550e8400-e29b-41d4-a716-446655440000%2F..%2F..%2Fetc%2Fpasswd",
			want: "550e8400-e29b-41d4-a716-446655440000", // url.Parse decodes %2F → UUID is a clean segment
		},
		{
			name: "fragment injection",
			href: "/app/recording/550e8400-e29b-41d4-a716-446655440000#malicious",
			want: "550e8400-e29b-41d4-a716-446655440000",
		},
		{
			name: "query injection",
			href: "/app/recording/550e8400-e29b-41d4-a716-446655440000?q=a&evil=b",
			want: "550e8400-e29b-41d4-a716-446655440000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractMeetingID(tt.href)
			if got != tt.want {
				t.Errorf("extractMeetingID(%q) = %q, want %q", tt.href, got, tt.want)
			}
		})
	}
}
