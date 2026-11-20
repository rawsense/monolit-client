package conf

import (
	"strings"
	"testing"
)

func TestBuildAWG(t *testing.T) {
	got, err := buildAWG(map[string]string{
		"Jc": "6", "Jmin": "10", "Jmax": "50", "S1": "12", "S2": "12", "S3": "12", "S4": "12",
		"H1": "1", "H2": "100-200", "H3": "3", "H4": "4",
		"I1":                  "<r 2><b 0x8580>",
		"HeaderProtectionKey": "48JrlBxyMKy7VwNgNZNiThShy+5hxJa0e+h8Oig6gEc=",
		"RekeyAfterTime":      "100-120", "RandomTrailers": "on", "disable_cookies": "off",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "jc=6\njmin=10\njmax=50\ns1=12\ns2=12\ns3=12\ns4=12\nh1=1\nh2=100-200\nh3=3\nh4=4\ni1=<r 2><b 0x8580>\n" +
		"header_protection_key=e3c26b941c7230acbb5703603593624e14a1cbee61c496b47be87c3a283a8047\n" +
		"rekey_after_time=100-120\nrandom_trailers=true\ndisable_cookies=false\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if s, err := buildAWG(nil); err != nil || s != "" {
		t.Fatalf("empty: %q %v", s, err)
	}
	for name, bad := range map[string]map[string]string{
		"unknown key":     {"Evil": "1"},
		"newline":         {"Jc": "1\nprivate_key=00"},
		"peer key":        {"public_key": "00"},
		"bad number":      {"S1": "x"},
		"bad range":       {"H1": "1-"},
		"bad bool":        {"RandomTrailers": "maybe"},
		"short key":       {"HeaderProtectionKey": "abcd"},
		"bad chain":       {"I1": "<b 0x1>"},
		"chain injection": {"I1": "<r 2>\njc=1"},
		"set twice":       {"Jc": "1", "jc": "2"},
	} {
		if s, err := buildAWG(bad); err == nil {
			t.Errorf("%s: accepted as %q", name, strings.ReplaceAll(s, "\n", "|"))
		}
	}
}
