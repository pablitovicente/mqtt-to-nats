package topics

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTopicToSubject(t *testing.T) {
	tests := []struct {
		prefix string
		topic  string
		want   string
	}{
		// NATS's own rules.
		{topic: "telemetry/device42/temperature", want: "telemetry.device42.temperature"},
		{topic: "/load", want: "/.load"},
		{topic: "foo//bar", want: "foo./.bar"},
		{topic: "foo/", want: "foo./"},
		{topic: "/", want: "/./"},
		{topic: "a.b/c", want: "a//b.c"},
		{topic: "a/b+c/d#", want: "a.b+c.d#"},
		{topic: "a/b*c/d>", want: "a.b*c.d>"},

		// Our escapes.
		{topic: "/floor/foo bar baz/modbus/48484", want: "/.floor.foo/20bar/20baz.modbus.48484"},
		{topic: "a/*/b", want: "a./2a.b"},
		{topic: ">", want: "/3e"},
		{topic: "tab\there", want: "tab/09here"},
		{topic: "del\x7f", want: "del/7f"},
		{topic: "nul\x00", want: "nul/00"},
		{topic: "bad\xffutf8", want: "bad/ffutf8"},
		{topic: "replacement�", want: "replacement/ef/bf/bd"},
		{topic: "ünïcode/ok", want: "ünïcode.ok"},

		// Prefix.
		{prefix: "mqtt", topic: "telemetry/temperature", want: "mqtt.telemetry.temperature"},
		{prefix: "mqtt.bridge", topic: "/load", want: "mqtt.bridge./.load"},
	}

	for _, test := range tests {
		got := TopicToSubject(test.prefix, test.topic)
		if got != test.want {
			t.Errorf("TopicToSubject(%q, %q) = %q, want %q", test.prefix, test.topic, got, test.want)
		}
	}
}

func TestSubjectToTopic_RejectsSubjectsWeDontMake(t *testing.T) {
	for _, subject := range []string{"a/b", "a/2", "a/zz"} {
		if topic, err := SubjectToTopic(subject); err == nil {
			t.Errorf("SubjectToTopic(%q) = %q, want an error", subject, topic)
		}
	}
}

// FuzzTopicToSubject checks that every topic gives a subject NATS accepts for publishing, and
// that SubjectToTopic gives back the exact topic.
func FuzzTopicToSubject(f *testing.F) {
	for _, seed := range []string{"a/b", "/a", "a//b", "a/", "/", "a.b", "a b", "*", "a/>/b", "\x7f", "\xff", "�", "a/2e"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, topic string) {
		if topic == "" {
			return
		}

		subject := TopicToSubject("", topic)
		if problem := publishSubjectProblem(subject); problem != "" {
			t.Fatalf("topic %q gave subject %q: %s", topic, subject, problem)
		}

		roundTrip, err := SubjectToTopic(subject)
		if err != nil {
			t.Fatalf("topic %q gave subject %q, which SubjectToTopic rejects: %v", topic, subject, err)
		}
		if roundTrip != topic {
			t.Fatalf("topic %q gave subject %q, which SubjectToTopic turns into %q", topic, subject, roundTrip)
		}
	})
}

// publishSubjectProblem returns why NATS would refuse or mishandle subject, or "" if it is
// fine. It follows isValidSubject (with its rune check) and subjectIsLiteral in nats-server's
// server/sublist.go.
func publishSubjectProblem(subject string) string {
	if !utf8.ValidString(subject) || strings.ContainsRune(subject, utf8.RuneError) {
		return "not valid UTF-8, or contains U+FFFD"
	}

	if strings.ContainsAny(subject, "\x00\x7f") {
		return "contains NUL or DEL"
	}

	for token := range strings.SplitSeq(subject, ".") {
		switch {
		case token == "":
			return "has an empty token"
		case token == "*" || token == ">":
			return "has a wildcard token"
		case strings.ContainsAny(token, " \t\n\r\f"):
			return "contains whitespace"
		}
	}

	return ""
}
