// Package proto contains the wire-level pieces of RFC 42TAP shared by the
// server and the clients: line framing, command parsing and error codes.
package proto

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Version is the protocol version announced in the greeting.
const Version = 1

// MaxLineLength is the maximum accepted size of one message (RFC 42TAP 9.4).
const MaxLineLength = 1024

// Greeting is the first line sent by the server (RFC 42TAP 3.2).
var Greeting = fmt.Sprintf("OK hello proto=%d", Version)

// Error is a protocol error: "ERR <code> <message>".
type Error struct {
	Code    int
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("ERR %d %s", e.Code, e.Message) }

// Standard error codes from RFC 42TAP section 8.2.
var (
	ErrNameInUse          = &Error{201, "NAME_IN_USE"}
	ErrNoExit             = &Error{301, "NO_EXIT"}
	ErrNotInGroup         = &Error{401, "NOT_IN_GROUP"}
	ErrAlreadyInGroup     = &Error{402, "ALREADY_IN_GROUP"}
	ErrItemNotFound       = &Error{404, "ITEM_NOT_FOUND"}
	ErrItemNotInInventory = &Error{404, "ITEM_NOT_IN_INVENTORY"}
	ErrNPCNotFound        = &Error{404, "NPC_NOT_FOUND"}
	ErrNPCNotHostile      = &Error{405, "NPC_NOT_HOSTILE"}
	ErrNoQuestAvailable   = &Error{406, "NO_QUEST_AVAILABLE"}
	ErrConnectionFailed   = &Error{900, "CONNECTION_FAILED"}
	ErrSendFailed         = &Error{901, "SEND_FAILED"}
)

// Extension error codes (documented in the README, "Protocol Implementation").
var (
	ErrInvalidName      = &Error{202, "INVALID_NAME"}
	ErrAlreadyConnected = &Error{203, "ALREADY_CONNECTED"}
	ErrInCombat         = &Error{303, "IN_COMBAT"}
	ErrMalformed        = &Error{400, "MALFORMED_COMMAND"}
	ErrUnknownCommand   = &Error{400, "UNKNOWN_COMMAND"}
	ErrLineTooLong      = &Error{400, "LINE_TOO_LONG"}
	ErrInvalidEncoding  = &Error{400, "INVALID_ENCODING"}
	ErrNotAuthenticated = &Error{403, "NOT_AUTHENTICATED"}
	ErrPlayerNotFound   = &Error{404, "PLAYER_NOT_FOUND"}
	ErrQuestNotFound    = &Error{404, "QUEST_NOT_FOUND"}
	ErrItemNotObtain    = &Error{405, "ITEM_NOT_OBTAINABLE"}
	ErrNotInvited       = &Error{407, "NOT_INVITED"}
	ErrNotInCombat      = &Error{408, "NOT_IN_COMBAT"}
	ErrItemNotUsable    = &Error{409, "ITEM_NOT_USABLE"}
	ErrRateLimited      = &Error{429, "RATE_LIMITED"}
	ErrServerFull       = &Error{902, "SERVER_FULL"}
	ErrStorage          = &Error{903, "STORAGE_UNAVAILABLE"}
)

// Command is a parsed client command line.
type Command struct {
	Name string   // upper-cased command name
	Args string   // raw argument string (trimmed)
	Raw  string   // the complete line as received
	Word []string // Args split on whitespace
}

// ParseCommand splits a line into a command name and its arguments.
// Command names are case-insensitive (RFC 42TAP 4.2).
func ParseCommand(line string) (Command, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return Command{}, ErrMalformed
	}
	name, args, _ := strings.Cut(line, " ")
	for _, r := range name {
		if r > unicode.MaxASCII || !unicode.IsLetter(r) && r != '_' {
			return Command{}, ErrMalformed
		}
	}
	args = strings.TrimSpace(args)
	return Command{
		Name: strings.ToUpper(name),
		Args: args,
		Raw:  line,
		Word: strings.Fields(args),
	}, nil
}

// ValidateLine checks encoding and control characters of a received line.
func ValidateLine(line string) error {
	if !utf8.ValidString(line) {
		return ErrInvalidEncoding
	}
	for _, r := range line {
		if r != '\t' && unicode.IsControl(r) {
			return ErrMalformed
		}
	}
	return nil
}

// ValidUsername reports whether a username is acceptable: 1-16 Unicode
// letters, digits, '_' or '-'.
func ValidUsername(name string) bool {
	n := utf8.RuneCountInString(name)
	if n == 0 || n > 16 {
		return false
	}
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

// ErrIdle is returned by a transport when the client sent nothing for the
// idle timeout; the session closes with reason "idle_timeout".
var ErrIdle = errors.New("idle timeout")

// ErrTooLong is returned by LineReader when a line exceeds the limit. The
// offending line has been fully consumed and reading can continue.
var ErrTooLong = errors.New("line too long")

// LineReader frames a TCP stream into LF-terminated lines, handling
// fragmentation and coalescing (RFC 42TAP 9.2).
type LineReader struct {
	r   *bufio.Reader
	max int
}

// NewLineReader wraps r; max <= 0 means no limit.
func NewLineReader(r io.Reader, max int) *LineReader {
	return &LineReader{r: bufio.NewReaderSize(r, 4096), max: max}
}

// ReadLine returns the next line without its terminator (a trailing CR is
// also removed, for telnet/netcat friendliness).
func (lr *LineReader) ReadLine() (string, error) {
	var buf []byte
	tooLong := false
	for {
		chunk, err := lr.r.ReadSlice('\n')
		if !tooLong {
			buf = append(buf, chunk...)
			if lr.max > 0 && len(buf) > lr.max+1 {
				tooLong = true
				buf = nil
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			if err == io.EOF && len(buf) > 0 && !tooLong {
				return trimEOL(buf), nil
			}
			return "", err
		}
		if tooLong {
			return "", ErrTooLong
		}
		return trimEOL(buf), nil
	}
}

func trimEOL(b []byte) string {
	s := string(b)
	s = strings.TrimSuffix(s, "\n")
	return strings.TrimSuffix(s, "\r")
}

// Response kinds for client-side classification.
const (
	KindOK    = "OK"
	KindErr   = "ERR"
	KindEvent = "EVT"
)

// Classify returns the kind of a server line and the payload after the
// keyword.
func Classify(line string) (kind, payload string) {
	head, rest, _ := strings.Cut(line, " ")
	switch head {
	case KindOK, KindErr, KindEvent:
		return head, rest
	}
	return "", line
}
