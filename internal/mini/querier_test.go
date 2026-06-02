package mini

import (
	"testing"
)

func TestGlobToRE2(t *testing.T) {
	tests := []struct {
		glob string
		want string
	}{
		{"cluster*", "cluster.*"},
		{"*hostname*", ".*hostname.*"},
		{"test?case", "test.case"},
		{"exact", "exact"},
		{"a.b", `a\.b`},
		{"foo+bar", `foo\+bar`},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.glob, func(t *testing.T) {
			got := globToRE2(tt.glob)
			if got != tt.want {
				t.Errorf("globToRE2(%q) = %q, want %q", tt.glob, got, tt.want)
			}
		})
	}
}

func TestBuildQueryExpr(t *testing.T) {
	tests := []struct {
		name      string
		pattern   string
		path      string
		testsOnly bool
		want      string
	}{
		{
			name:      "empty defaults to all workspace",
			testsOnly: false,
			want:      "//...",
		},
		{
			name:      "tests only without pattern",
			testsOnly: true,
			want:      "tests(//...)",
		},
		{
			name:      "pattern only",
			pattern:   "cluster*",
			testsOnly: false,
			want:      `filter("cluster.*", //...)`,
		},
		{
			name:      "pattern + path",
			pattern:   "cluster*",
			path:      "test/extensions/clusters/...",
			testsOnly: false,
			want:      `filter("cluster.*", //test/extensions/clusters/...)`,
		},
		{
			name:      "tests + pattern + path",
			pattern:   "*hostname*",
			path:      "test/extensions/clusters/dynamic_modules/...",
			testsOnly: true,
			want:      `filter(".*hostname.*", tests(//test/extensions/clusters/dynamic_modules/...))`,
		},
		{
			name:      "path already has // prefix",
			path:      "//test/foo/...",
			testsOnly: false,
			want:      "//test/foo/...",
		},
		{
			name:      "path without trailing ...",
			path:      "test/foo",
			testsOnly: false,
			want:      "//test/foo/...",
		},
		{
			name:      "path with specific target stays unchanged",
			path:      "//test/foo:bar",
			testsOnly: false,
			want:      "//test/foo:bar",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildQueryExpr(tt.pattern, tt.path, tt.testsOnly)
			if got != tt.want {
				t.Errorf("buildQueryExpr(%q, %q, %v) = %q, want %q",
					tt.pattern, tt.path, tt.testsOnly, got, tt.want)
			}
		})
	}
}

func TestTestTypeSizeAttr(t *testing.T) {
	tests := []struct {
		testType string
		want     string
	}{
		{"unit", "small|medium"},
		{"Unit", "small|medium"},
		{"UNIT", "small|medium"},
		{"integration", "large|enormous"},
		{"Integration", "large|enormous"},
		{"", ""},
		{"other", ""},
	}
	for _, tt := range tests {
		t.Run(tt.testType, func(t *testing.T) {
			got := testTypeSizeAttr(tt.testType)
			if got != tt.want {
				t.Errorf("testTypeSizeAttr(%q) = %q, want %q", tt.testType, got, tt.want)
			}
		})
	}
}
