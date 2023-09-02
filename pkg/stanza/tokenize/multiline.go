// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tokenize // import "github.com/open-telemetry/opentelemetry-collector-contrib/pkg/stanza/tokenize"

import (
	"bufio"
	"bytes"
	"fmt"
	"regexp"

	"golang.org/x/text/encoding"
)

// Multiline consists of splitFunc and variables needed to perform force flush
type Multiline struct {
	SplitFunc bufio.SplitFunc
}

// NewMultilineConfig creates a new Multiline config
func NewMultilineConfig() MultilineConfig {
	return MultilineConfig{
		LineStartPattern: "",
		LineEndPattern:   "",
	}
}

// MultilineConfig is the configuration of a multiline helper
type MultilineConfig struct {
	LineStartPattern string `mapstructure:"line_start_pattern"`
	LineEndPattern   string `mapstructure:"line_end_pattern"`
}

// Build will build a Multiline operator.
func (c MultilineConfig) Build(enc encoding.Encoding, flushAtEOF, preserveLeadingWhitespaces, preserveTrailingWhitespaces bool, maxLogSize int) (bufio.SplitFunc, error) {
	return c.getSplitFunc(enc, flushAtEOF, maxLogSize, preserveLeadingWhitespaces, preserveTrailingWhitespaces)
}

// getSplitFunc returns split function for bufio.Scanner basing on configured pattern
func (c MultilineConfig) getSplitFunc(enc encoding.Encoding, flushAtEOF bool, maxLogSize int, preserveLeadingWhitespaces, preserveTrailingWhitespaces bool) (bufio.SplitFunc, error) {
	endPattern := c.LineEndPattern
	startPattern := c.LineStartPattern

	var (
		splitFunc bufio.SplitFunc
		err       error
	)

	switch {
	case endPattern != "" && startPattern != "":
		return nil, fmt.Errorf("only one of line_start_pattern or line_end_pattern can be set")
	case enc == encoding.Nop && (endPattern != "" || startPattern != ""):
		return nil, fmt.Errorf("line_start_pattern or line_end_pattern should not be set when using nop encoding")
	case enc == encoding.Nop:
		return NoSplitFunc(maxLogSize), nil
	case endPattern == "" && startPattern == "":
		splitFunc, err = NewlineSplitFunc(enc, flushAtEOF, getTrimFunc(preserveLeadingWhitespaces, preserveTrailingWhitespaces))
		if err != nil {
			return nil, err
		}
	case endPattern != "":
		re, err := regexp.Compile("(?m)" + c.LineEndPattern)
		if err != nil {
			return nil, fmt.Errorf("compile line end regex: %w", err)
		}
		splitFunc = LineEndSplitFunc(re, flushAtEOF, getTrimFunc(preserveLeadingWhitespaces, preserveTrailingWhitespaces))
	case startPattern != "":
		re, err := regexp.Compile("(?m)" + c.LineStartPattern)
		if err != nil {
			return nil, fmt.Errorf("compile line start regex: %w", err)
		}
		splitFunc = LineStartSplitFunc(re, flushAtEOF, getTrimFunc(preserveLeadingWhitespaces, preserveTrailingWhitespaces))
	default:
		return nil, fmt.Errorf("unreachable")
	}
	return splitFunc, nil
}

// LineStartSplitFunc creates a bufio.SplitFunc that splits an incoming stream into
// tokens that start with a match to the regex pattern provided
func LineStartSplitFunc(re *regexp.Regexp, flushAtEOF bool, trimFunc trimFunc) bufio.SplitFunc {
	return func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		firstLoc := re.FindIndex(data)
		if firstLoc == nil {
			// Flush if no more data is expected
			if len(data) != 0 && atEOF && flushAtEOF {
				token = trimFunc(data)
				advance = len(data)
				return
			}
			return 0, nil, nil // read more data and try again.
		}
		firstMatchStart := firstLoc[0]
		firstMatchEnd := firstLoc[1]

		if firstMatchStart != 0 {
			// the beginning of the file does not match the start pattern, so return a token up to the first match so we don't lose data
			advance = firstMatchStart
			token = trimFunc(data[0:firstMatchStart])

			// return if non-matching pattern is not only whitespaces
			if token != nil {
				return
			}
		}

		if firstMatchEnd == len(data) {
			// the first match goes to the end of the bufer, so don't look for a second match
			return 0, nil, nil
		}

		// Flush if no more data is expected
		if atEOF && flushAtEOF {
			token = trimFunc(data)
			advance = len(data)
			return
		}

		secondLocOfset := firstMatchEnd + 1
		secondLoc := re.FindIndex(data[secondLocOfset:])
		if secondLoc == nil {
			return 0, nil, nil // read more data and try again
		}
		secondMatchStart := secondLoc[0] + secondLocOfset

		advance = secondMatchStart                               // start scanning at the beginning of the second match
		token = trimFunc(data[firstMatchStart:secondMatchStart]) // the token begins at the first match, and ends at the beginning of the second match
		err = nil
		return
	}
}

// LineEndSplitFunc creates a bufio.SplitFunc that splits an incoming stream into
// tokens that end with a match to the regex pattern provided
func LineEndSplitFunc(re *regexp.Regexp, flushAtEOF bool, trimFunc trimFunc) bufio.SplitFunc {
	return func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		loc := re.FindIndex(data)
		if loc == nil {
			// Flush if no more data is expected
			if len(data) != 0 && atEOF && flushAtEOF {
				token = trimFunc(data)
				advance = len(data)
				return
			}
			return 0, nil, nil // read more data and try again
		}

		// If the match goes up to the end of the current bufer, do another
		// read until we can capture the entire match
		if loc[1] == len(data)-1 && !atEOF {
			return 0, nil, nil
		}

		advance = loc[1]
		token = trimFunc(data[:loc[1]])
		err = nil
		return
	}
}

// NewlineSplitFunc splits log lines by newline, just as bufio.ScanLines, but
// never returning an token using EOF as a terminator
func NewlineSplitFunc(enc encoding.Encoding, flushAtEOF bool, trimFunc trimFunc) (bufio.SplitFunc, error) {
	newline, err := encodedNewline(enc)
	if err != nil {
		return nil, err
	}

	carriageReturn, err := encodedCarriageReturn(enc)
	if err != nil {
		return nil, err
	}

	return func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		if atEOF && len(data) == 0 {
			return 0, nil, nil
		}

		if i := bytes.Index(data, newline); i >= 0 {
			// We have a full newline-terminated line.
			token = bytes.TrimSuffix(data[:i], carriageReturn)

			return i + len(newline), trimFunc(token), nil
		}

		// Flush if no more data is expected
		if atEOF && flushAtEOF {
			token = trimFunc(data)
			advance = len(data)
			return
		}

		// Request more data.
		return 0, nil, nil
	}, nil
}

// NoSplitFunc doesn't split any of the bytes, it reads in all of the bytes and returns it all at once. This is for when the encoding is nop
func NoSplitFunc(maxLogSize int) bufio.SplitFunc {
	return func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		if len(data) >= maxLogSize {
			return maxLogSize, data[:maxLogSize], nil
		}

		if !atEOF {
			return 0, nil, nil
		}

		if len(data) == 0 {
			return 0, nil, nil
		}
		return len(data), data, nil
	}
}

func encodedNewline(enc encoding.Encoding) ([]byte, error) {
	out := make([]byte, 10)
	nDst, _, err := enc.NewEncoder().Transform(out, []byte{'\n'}, true)
	return out[:nDst], err
}

func encodedCarriageReturn(enc encoding.Encoding) ([]byte, error) {
	out := make([]byte, 10)
	nDst, _, err := enc.NewEncoder().Transform(out, []byte{'\r'}, true)
	return out[:nDst], err
}

type trimFunc func([]byte) []byte

func noTrim(token []byte) []byte {
	return token
}

func trimLeadingWhitespacesFunc(data []byte) []byte {
	// TrimLeft to strip EOF whitespaces in case of using $ in regex
	// For some reason newline and carriage return are being moved to beginning of next log
	token := bytes.TrimLeft(data, "\r\n\t ")
	if token == nil {
		return []byte{}
	}
	return token
}

func trimTrailingWhitespacesFunc(data []byte) []byte {
	// TrimRight to strip all whitespaces from the end of log
	token := bytes.TrimRight(data, "\r\n\t ")
	if token == nil {
		return []byte{}
	}
	return token
}

func trimWhitespacesFunc(data []byte) []byte {
	return trimLeadingWhitespacesFunc(trimTrailingWhitespacesFunc(data))
}

func getTrimFunc(preserveLeadingWhitespaces, preserveTrailingWhitespaces bool) trimFunc {
	if preserveLeadingWhitespaces && preserveTrailingWhitespaces {
		return noTrim
	}
	if preserveLeadingWhitespaces {
		return trimTrailingWhitespacesFunc
	}
	if preserveTrailingWhitespaces {
		return trimLeadingWhitespacesFunc
	}
	return trimWhitespacesFunc
}
