package sharedstate

import "testing"

func TestUsesShare(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want bool
	}{
		{"double quote", `local s = require("share")`, true},
		{"single quote", `local s = require('share')`, true},
		{"no paren", `require "share"`, true},
		{"spaces", `require (  "share"  )`, true},
		{"other module", `local n = require("network")`, false},
		{"none", `local x = 1`, false},
		{"line comment", `-- require("share")`, false},
		{"line comment trailing", `local x = 1 -- require("share")`, false},
		{"block comment", "--[[ require(\"share\") ]] local x = 1", false},
		{"long block comment", "--[==[ require('share') ]==]\nlocal x=1", false},
		{"real require after comment", "-- old: require('network')\nrequire('share')", true},
		{"require inside long string preserved", "local s = [[ hello ]]\nrequire('share')", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := UsesShare(c.src); got != c.want {
				t.Fatalf("UsesShare(%q) = %v, want %v", c.src, got, c.want)
			}
		})
	}
}

func TestStripLuaCommentsKeepsStrings(t *testing.T) {
	// 字符串中的 -- 不应被当作注释
	src := `local url = "http://a--b" -- comment`
	out := stripLuaComments(src)
	if !contains(out, `"http://a--b"`) {
		t.Fatalf("string content lost: %q", out)
	}
	if contains(out, "comment") {
		t.Fatalf("comment not stripped: %q", out)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
