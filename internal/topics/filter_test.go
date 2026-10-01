package topics

import (
	"slices"
	"testing"
)

func TestValidateFilter(t *testing.T) {
	valid := []string{"a", "a/b", "#", "+", "a/#", "a/+/b", "+/+/#", "/", "/#", "a b/c", "$SYS/#"}
	for _, filter := range valid {
		if err := ValidateFilter(filter); err != nil {
			t.Errorf("ValidateFilter(%q) = %v, want no error", filter, err)
		}
	}

	invalid := []string{"", "a/#/b", "a#", "a/b#", "a+", "a/+b/c", "#/a", "a\x00b", "bad\xff"}
	for _, filter := range invalid {
		if err := ValidateFilter(filter); err == nil {
			t.Errorf("ValidateFilter(%q) = nil, want an error", filter)
		}
	}
}

func TestFiltersOverlap(t *testing.T) {
	tests := []struct {
		first  string
		second string
		want   bool
	}{
		{"a/#", "a/b/#", true},
		{"a/#", "a", true},
		{"a/+", "a", false},
		{"a/+", "a/b", true},
		{"a/+/c", "a/b/d", false},
		{"a/b", "a/b", true},
		{"a/b", "a/c", false},
		{"telemetry/#", "events/#", false},
		{"+/temperature", "telemetry/+", true},
		{"#", "anything/at/all", true},
		{"#", "$SYS/#", false},
		{"+/x", "$SYS/x", false},
		{"$SYS/#", "$SYS/broker", true},
		{"/a", "a", false},
		{"/#", "+/a", true},
	}

	for _, test := range tests {
		if got := FiltersOverlap(test.first, test.second); got != test.want {
			t.Errorf("FiltersOverlap(%q, %q) = %v, want %v", test.first, test.second, got, test.want)
		}
		if got := FiltersOverlap(test.second, test.first); got != test.want {
			t.Errorf("FiltersOverlap(%q, %q) = %v, want %v", test.second, test.first, got, test.want)
		}
	}
}

func TestNeedsPrefix(t *testing.T) {
	for _, filter := range []string{"#", "+", "+/a", "$SYS/#", "$JS/API/#"} {
		if !NeedsPrefix(filter) {
			t.Errorf("NeedsPrefix(%q) = false, want true", filter)
		}
	}

	for _, filter := range []string{"a/#", "/#", "a/+/b", "a$/b"} {
		if NeedsPrefix(filter) {
			t.Errorf("NeedsPrefix(%q) = true, want false", filter)
		}
	}
}

func TestValidatePrefix(t *testing.T) {
	for _, prefix := range []string{"mqtt", "mqtt.bridge", "a-b_c"} {
		if err := ValidatePrefix(prefix); err != nil {
			t.Errorf("ValidatePrefix(%q) = %v, want no error", prefix, err)
		}
	}

	for _, prefix := range []string{"", ".mqtt", "mqtt.", "a..b", "a.*", "a.>", "a b", "a\x7f", "bad\xff"} {
		if err := ValidatePrefix(prefix); err == nil {
			t.Errorf("ValidatePrefix(%q) = nil, want an error", prefix)
		}
	}
}

func TestValidateSubject(t *testing.T) {
	for _, subject := range []string{"TELEMETRY", "mqtt.telemetry"} {
		if err := ValidateSubject(subject); err != nil {
			t.Errorf("ValidateSubject(%q) = %v, want no error", subject, err)
		}
	}

	for _, subject := range []string{"", "a.*", "a.>", "a b", "$JS.API.x"} {
		if err := ValidateSubject(subject); err == nil {
			t.Errorf("ValidateSubject(%q) = nil, want an error", subject)
		}
	}
}

func TestStreamSubjects(t *testing.T) {
	tests := []struct {
		filter string
		prefix string
		want   []string
	}{
		{filter: "telemetry/#", want: []string{"telemetry.>", "telemetry"}},
		{filter: "telemetry/+/temperature", want: []string{"telemetry.*.temperature"}},
		{filter: "a/+/#", want: []string{"a.*.>", "a.*"}},
		{filter: "/#", want: []string{"/.>"}},
		{filter: "/load", want: []string{"/.load"}},
		{filter: "floor/foo bar/#", want: []string{"floor.foo/20bar.>", "floor.foo/20bar"}},
		{filter: "#", prefix: "mqtt", want: []string{"mqtt.>"}},
		{filter: "$SYS/#", prefix: "mqtt", want: []string{"mqtt.$SYS.>", "mqtt.$SYS"}},
	}

	for _, test := range tests {
		got := StreamSubjects(test.filter, test.prefix)
		if !slices.Equal(got, test.want) {
			t.Errorf("StreamSubjects(%q, %q) = %q, want %q", test.filter, test.prefix, got, test.want)
		}
	}
}

func TestSubjectsOverlap(t *testing.T) {
	tests := []struct {
		first  string
		second string
		want   bool
	}{
		{"a.>", "a.x.>", true},
		{"a.>", "a", false},
		{"a", "a", true},
		{"a.*", "a.b", true},
		{"a.*", "a.b.c", false},
		{"a.b", "a.c", false},
		{"mqtt.telemetry.>", "mqtt.events.>", false},
		{"*.x", "a.>", true},
	}

	for _, test := range tests {
		if got := SubjectsOverlap(test.first, test.second); got != test.want {
			t.Errorf("SubjectsOverlap(%q, %q) = %v, want %v", test.first, test.second, got, test.want)
		}
		if got := SubjectsOverlap(test.second, test.first); got != test.want {
			t.Errorf("SubjectsOverlap(%q, %q) = %v, want %v", test.second, test.first, got, test.want)
		}
	}
}
