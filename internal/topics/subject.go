// Package topics turns MQTT topics and topic filters into NATS subjects, and checks the topic
// filters of the configured routes.
//
// The conversion follows the rules NATS server uses for its own MQTT support
// (mqttToNATSSubjectConversion and natsSubjectToMQTTTopic in nats-server's server/mqtt.go), so
// a topic NATS accepts gets the same subject here:
//
//   - '/' becomes '.' (foo/bar -> foo.bar)
//   - a '/' at the start, or right after another separator, becomes '/.' (/foo -> /.foo)
//   - a '/' at the end, or right before another '/', becomes './' (foo//bar -> foo./.bar)
//   - '.' becomes '//'
//
// NATS rejects topics it can't put in a subject. This package escapes those bytes instead, as
// '/' followed by two lowercase hex digits (a space becomes /20):
//
//   - space, tab, newline, carriage return and form feed, which NATS doesn't allow in subjects
//   - NUL, DEL, and bytes that aren't valid UTF-8 (or are U+FFFD), which make NATS's file
//     store treat its saved state as corrupt when it starts up
//   - a level that is exactly '*' or '>', which would be a wildcard in NATS
//
// NATS's rules only ever put '.', '/' or the end of the subject after a '/' that starts a
// pair, so a '/' followed by a hex digit can't be mistaken for one of them.
package topics

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const hexDigits = "0123456789abcdef"

// TopicToSubject returns the NATS subject for an MQTT topic name. A non-empty prefix is put in
// front, followed by a '.'.
func TopicToSubject(prefix, topic string) string {
	return convert(prefix, topic, false)
}

// filterToSubject returns the NATS subject filter for an MQTT topic filter: '+' becomes '*'
// and '#' becomes '>'. The filter must already have passed ValidateFilter.
func filterToSubject(prefix, filter string) string {
	return convert(prefix, filter, true)
}

// convert does the work for TopicToSubject and filterToSubject. With isFilter, a level that is
// exactly '+' or '#' becomes a NATS wildcard; otherwise '+' and '#' are copied as they are.
func convert(prefix, topic string, isFilter bool) string {
	var subject strings.Builder
	subject.Grow(len(prefix) + len(topic) + 8)

	if prefix != "" {
		subject.WriteString(prefix)
		subject.WriteByte('.')
	}

	// lastWritten is the last byte written for the topic itself, not the prefix. NATS's rules
	// for '/' depend on it.
	var lastWritten byte

	for i := 0; i < len(topic); {
		character := topic[i]

		switch {
		case character == '/':
			switch {
			case i == 0 || lastWritten == '.':
				subject.WriteString("/.")
				lastWritten = '.'
			case i == len(topic)-1 || topic[i+1] == '/':
				subject.WriteString("./")
				lastWritten = '/'
			default:
				subject.WriteByte('.')
				lastWritten = '.'
			}
			i++

		case character == '.':
			subject.WriteString("//")
			lastWritten = '/'
			i++

		case isFilter && isWholeLevel(topic, i) && (character == '+' || character == '#'):
			if character == '+' {
				subject.WriteByte('*')
			} else {
				subject.WriteByte('>')
			}
			lastWritten = 'x'
			i++

		case isWholeLevel(topic, i) && (character == '*' || character == '>'):
			writeEscaped(&subject, character)
			lastWritten = 'x'
			i++

		case character < utf8.RuneSelf:
			if needsEscape(character) {
				writeEscaped(&subject, character)
				lastWritten = 'x'
			} else {
				subject.WriteByte(character)
				lastWritten = character
			}
			i++

		default:
			decoded, size := utf8.DecodeRuneInString(topic[i:])
			if decoded == utf8.RuneError {
				for _, invalidByte := range []byte(topic[i : i+size]) {
					writeEscaped(&subject, invalidByte)
				}
			} else {
				subject.WriteString(topic[i : i+size])
			}
			lastWritten = 'x'
			i += size
		}
	}

	// A subject can't end with an empty token. NATS closes it the same way.
	if lastWritten == '.' {
		subject.WriteByte('/')
	}

	return subject.String()
}

// isWholeLevel reports whether the byte at index is a topic level on its own, with a '/' or
// the start or end of the topic on both sides.
func isWholeLevel(topic string, index int) bool {
	startsLevel := index == 0 || topic[index-1] == '/'
	endsLevel := index == len(topic)-1 || topic[index+1] == '/'

	return startsLevel && endsLevel
}

// needsEscape reports whether an ASCII byte can't go into a NATS subject as it is.
func needsEscape(character byte) bool {
	switch character {
	case ' ', '\t', '\n', '\r', '\f', 0x00, 0x7f:
		return true
	}

	return false
}

func writeEscaped(subject *strings.Builder, character byte) {
	subject.WriteByte('/')
	subject.WriteByte(hexDigits[character>>4])
	subject.WriteByte(hexDigits[character&0x0f])
}

// SubjectToTopic returns the MQTT topic a subject made by TopicToSubject came from. The prefix,
// if any, must already be removed. It returns an error for a subject TopicToSubject can't
// produce.
func SubjectToTopic(subject string) (string, error) {
	var topic strings.Builder
	topic.Grow(len(subject))

	for i := 0; i < len(subject); i++ {
		character := subject[i]

		switch character {
		case '.':
			topic.WriteByte('/')

		case '/':
			if i == len(subject)-1 {
				// The './' NATS adds after a trailing '/'. The '.' already wrote the '/'.
				continue
			}

			next := subject[i+1]
			switch {
			case next == '.':
				topic.WriteByte('/')
				i++
			case next == '/':
				topic.WriteByte('.')
				i++
			case i+2 < len(subject) && isHexDigit(next) && isHexDigit(subject[i+2]):
				topic.WriteByte(hexValue(next)<<4 | hexValue(subject[i+2]))
				i += 2
			default:
				return "", fmt.Errorf("subject %q has a '/' at position %d that is not followed by '.', '/' or two hex digits", subject, i)
			}

		default:
			topic.WriteByte(character)
		}
	}

	return topic.String(), nil
}

func isHexDigit(character byte) bool {
	return ('0' <= character && character <= '9') || ('a' <= character && character <= 'f')
}

func hexValue(character byte) byte {
	if character <= '9' {
		return character - '0'
	}

	return character - 'a' + 10
}
