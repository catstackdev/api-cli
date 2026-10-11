package cli

import (
	"reflect"
	"testing"
)

func TestSplitWordsEdgeCases(t *testing.T) {
	cases := map[string][]string{
		"":                         nil,
		"   ":                      nil,
		"jless":                    {"jless"},
		"a  b\tc":                  {"a", "b", "c"},
		`nvim -R -c 'set ft=json'`: {"nvim", "-R", "-c", "set ft=json"},
		`say "two words" x`:        {"say", "two words", "x"},
		`a '' b`:                   {"a", "", "b"}, // an empty quoted word is still a word
		`a 'b c`:                   {"a", "b c"},   // an unterminated quote runs to the end
	}
	for in, want := range cases {
		if got := splitWords(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitWords(%q) = %q, want %q", in, got, want)
		}
	}
}
