package main

import (
	"strings"
	"testing"
)

func TestConvertWebhookEvent_NewMessageDM(t *testing.T) {
	body := []byte(`{
		"type": "new-message",
		"data": {
			"guid": "msg-1",
			"text": "hello there",
			"isFromMe": false,
			"dateCreated": 1700000000000,
			"handle": {"address": "+15551234567", "service": "iMessage"},
			"chats": [{"guid": "iMessage;-;+15551234567", "isGroup": false}]
		}
	}`)
	msg := convertWebhookEvent(body, nil, nil)
	if msg == nil {
		t.Fatal("expected message, got nil")
	}
	if msg.GetChannel() != "bluebubbles" {
		t.Errorf("channel = %q", msg.GetChannel())
	}
	if msg.GetSenderId() != "+15551234567" {
		t.Errorf("sender = %q", msg.GetSenderId())
	}
	if msg.GetRoomId() != "iMessage;-;+15551234567" {
		t.Errorf("room = %q", msg.GetRoomId())
	}
	if msg.GetText() != "hello there" {
		t.Errorf("text = %q", msg.GetText())
	}
	if msg.GetTsMs() != 1700000000000 {
		t.Errorf("ts = %d", msg.GetTsMs())
	}
}

func TestConvertWebhookEvent_SkipsEchoesAndOtherTypes(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"isFromMe", `{"type":"new-message","data":{"text":"x","isFromMe":true,"chats":[{"guid":"g"}]}}`},
		{"empty text", `{"type":"new-message","data":{"text":"   ","chats":[{"guid":"g"}]}}`},
		{"no chats", `{"type":"new-message","data":{"text":"hi"}}`},
		{"updated-message", `{"type":"updated-message","data":{"text":"hi","chats":[{"guid":"g"}]}}`},
		{"unknown type", `{"type":"chat-read-status-changed","data":{}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if msg := convertWebhookEvent([]byte(tc.body), nil, nil); msg != nil {
				t.Errorf("expected nil for %s, got %+v", tc.name, msg)
			}
		})
	}
}

func TestConvertWebhookEvent_DMAllowlist(t *testing.T) {
	allow := buildAllowSet("allowlist", []string{"+15551234567"})
	good := []byte(`{
		"type":"new-message",
		"data":{
			"text":"hi",
			"handle":{"address":"+15551234567"},
			"chats":[{"guid":"iMessage;-;+15551234567","isGroup":false}]
		}
	}`)
	bad := []byte(`{
		"type":"new-message",
		"data":{
			"text":"hi",
			"handle":{"address":"+15559999999"},
			"chats":[{"guid":"iMessage;-;+15559999999","isGroup":false}]
		}
	}`)
	if msg := convertWebhookEvent(good, allow, nil); msg == nil {
		t.Error("allowlisted sender should pass")
	}
	if msg := convertWebhookEvent(bad, allow, nil); msg != nil {
		t.Error("non-allowlisted sender should be dropped")
	}
}

func TestConvertWebhookEvent_GroupAllowlistGate(t *testing.T) {
	// DM allowlist must not apply to group messages, and vice versa.
	allowDM := buildAllowSet("allowlist", []string{"+15551234567"})
	allowGroup := buildAllowSet("allowlist", []string{"+15559999999"})

	groupMsg := []byte(`{
		"type":"new-message",
		"data":{
			"text":"hi all",
			"handle":{"address":"+15559999999"},
			"chats":[{"guid":"iMessage;+;abcd","isGroup":true}],
			"groupTitle":"Crew"
		}
	}`)
	if msg := convertWebhookEvent(groupMsg, allowDM, allowGroup); msg == nil {
		t.Error("group-allowlisted sender should pass on a group chat")
	} else {
		if msg.GetDisplayName() != "Crew" {
			t.Errorf("display = %q, want %q", msg.GetDisplayName(), "Crew")
		}
		if msg.GetRoomId() != "iMessage;+;abcd" {
			t.Errorf("room = %q", msg.GetRoomId())
		}
	}

	// Same sender, but coming via a DM chat — the DM allowlist
	// (which doesn't include them) should bite.
	dmFromGroupMember := []byte(`{
		"type":"new-message",
		"data":{
			"text":"hi",
			"handle":{"address":"+15559999999"},
			"chats":[{"guid":"iMessage;-;+15559999999","isGroup":false}]
		}
	}`)
	if msg := convertWebhookEvent(dmFromGroupMember, allowDM, allowGroup); msg != nil {
		t.Error("group-allowlisted sender should NOT pass via DM allowlist")
	}
}

func TestBuildAllowSet(t *testing.T) {
	cases := []struct {
		name     string
		policy   string
		list     []string
		wantNil  bool // open policy → nil
		contains []string
		excludes []string
	}{
		{"open returns nil (accept-all)", "open", []string{"+1"}, true, nil, nil},
		{"allowlist populates", "allowlist", []string{"+1", "+2", " "}, false, []string{"+1", "+2"}, []string{"+3"}},
		{"empty policy treated as allowlist", "", []string{"+1"}, false, []string{"+1"}, []string{"+2"}},
		{"disabled returns empty", "disabled", []string{"+1"}, false, nil, []string{"+1"}},
		{"pairing returns empty (deny-all until paired)", "pairing", []string{"+1"}, false, nil, []string{"+1"}},
		{"unknown policy returns empty (conservative)", "weird", []string{"+1"}, false, nil, []string{"+1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildAllowSet(tc.policy, tc.list)
			if tc.wantNil {
				if got != nil {
					t.Errorf("got %v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("got nil, want non-nil")
			}
			for _, c := range tc.contains {
				if _, ok := got[c]; !ok {
					t.Errorf("missing %q", c)
				}
			}
			for _, c := range tc.excludes {
				if _, ok := got[c]; ok {
					t.Errorf("unexpected %q", c)
				}
			}
		})
	}
}

func TestDefaultChatGUIDFromAllow(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{}, ""},
		{[]string{"+15551234567"}, "iMessage;-;+15551234567"},
		{[]string{"  ", "user@example.com"}, "iMessage;-;user@example.com"},
	}
	for _, tc := range cases {
		if got := defaultChatGUIDFromAllow(tc.in); got != tc.want {
			t.Errorf("defaultChatGUIDFromAllow(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestConvertWebhookEvent_BadJSON(t *testing.T) {
	if msg := convertWebhookEvent([]byte("{not-json"), nil, nil); msg != nil {
		t.Error("malformed JSON should return nil")
	}
}

func TestNewTempGUID_Format(t *testing.T) {
	g, err := newTempGUID()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(g, "talon-") {
		t.Errorf("guid %q missing prefix", g)
	}
	// 16 bytes hex-encoded = 32 chars + "talon-" prefix.
	if len(g) != len("talon-")+32 {
		t.Errorf("guid len = %d", len(g))
	}
}
