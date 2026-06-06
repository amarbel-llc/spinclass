package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/amarbel-llc/spinclass/internal/chat"
)

// sendArgs marshals chat-send tool arguments.
func sendArgs(t *testing.T, kv map[string]string) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(kv)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return data
}

// storedMessages decodes every committed message in the isolated chatroom.
func storedMessages(t *testing.T) []chat.Message {
	t.Helper()
	msgs, err := chat.Read("reader/none", chat.ReadFilter{}, true)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	return msgs
}

func TestHandleChatSendSubjectAndBodyStored(t *testing.T) {
	chatEnv(t)

	res, err := handleChatSend(context.Background(), sendArgs(t, map[string]string{
		"subject": "rebase needed",
		"body":    "the schema migration landed\ndetails follow",
		"to":      "clown/peer",
	}), nil)
	if err != nil || res.IsErr {
		t.Fatalf("send: err=%v res=%+v", err, res)
	}

	msgs := storedMessages(t)
	if len(msgs) != 1 {
		t.Fatalf("stored: got %d messages, want 1", len(msgs))
	}
	if msgs[0].Subject != "rebase needed" || !strings.HasPrefix(msgs[0].Body, "the schema migration") {
		t.Fatalf("stored message: %+v", msgs[0])
	}
}

func TestHandleChatSendRejectsOverCapSubject(t *testing.T) {
	chatEnv(t)

	res, err := handleChatSend(context.Background(), sendArgs(t, map[string]string{
		"subject": strings.Repeat("x", chat.SubjectMaxLen+1),
		"body":    "detail",
	}), nil)
	if err != nil {
		t.Fatalf("handleChatSend: %v", err)
	}
	if !res.IsErr {
		t.Fatalf("over-cap subject accepted: %q", res.Text)
	}
	if got := storedMessages(t); len(got) != 0 {
		t.Fatalf("over-cap subject was stored: %d messages", len(got))
	}
}

func TestHandleChatSendMessageAliasDerivesSubject(t *testing.T) {
	chatEnv(t)

	res, err := handleChatSend(context.Background(), sendArgs(t, map[string]string{
		"message": "first line summary\nrest of the detail",
	}), nil)
	if err != nil || res.IsErr {
		t.Fatalf("alias send: err=%v res=%+v", err, res)
	}

	msgs := storedMessages(t)
	if len(msgs) != 1 {
		t.Fatalf("stored: got %d messages, want 1", len(msgs))
	}
	if msgs[0].Body != "first line summary\nrest of the detail" {
		t.Fatalf("alias body: %q", msgs[0].Body)
	}
	if subject := msgs[0].DisplaySubject(); subject != "first line summary" {
		t.Fatalf("derived subject: %q", subject)
	}
}

func TestHandleChatSendRequiresContent(t *testing.T) {
	chatEnv(t)

	res, err := handleChatSend(context.Background(), sendArgs(t, map[string]string{"to": "a/b"}), nil)
	if err != nil {
		t.Fatalf("handleChatSend: %v", err)
	}
	if !res.IsErr {
		t.Fatal("contentless send accepted")
	}
}

func TestHandleChatSendEmitCarriesSubject(t *testing.T) {
	chatEnv(t)
	argsFile := t.TempDir() + "/args"
	t.Setenv("CLOWN_BIN", stubClownBin(t, argsFile, true))

	res, err := handleChatSend(context.Background(), sendArgs(t, map[string]string{
		"subject": "short summary",
		"body":    "very long body\nacross lines",
		"to":      "clown/peer",
	}), nil)
	if err != nil || res.IsErr {
		t.Fatalf("send: err=%v res=%+v", err, res)
	}

	argv := readFileString(t, argsFile)
	if !strings.Contains(argv, "--message\nshort summary\n") {
		t.Fatalf("emit must carry the subject, not the body:\n%s", argv)
	}
	if strings.Contains(argv, "very long body") {
		t.Fatalf("emit leaked the body into the wake line:\n%s", argv)
	}
}

func TestHandleChatReadRendersSubjectAndBody(t *testing.T) {
	chatEnv(t)

	if res, err := handleChatSend(context.Background(), sendArgs(t, map[string]string{
		"subject": "the summary",
		"body":    "the full body",
	}), nil); err != nil || res.IsErr {
		t.Fatalf("send: err=%v res=%+v", err, res)
	}

	res, err := handleChatRead(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil || res.IsErr {
		t.Fatalf("read: err=%v res=%+v", err, res)
	}
	if !strings.Contains(res.Text, "the summary") {
		t.Fatalf("read output missing subject: %q", res.Text)
	}
	if !strings.Contains(res.Text, "the full body") {
		t.Fatalf("read output missing body: %q", res.Text)
	}
}

// readFileString reads a file into a string, failing the test on error.
func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
