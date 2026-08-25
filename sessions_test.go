package main

import (
	"encoding/json"
	"net/netip"
	"testing"
)

func TestParseConntrackLineForLeiGodTarget(t *testing.T) {
	line := "ipv4 2 tcp 6 431999 ESTABLISHED src=192.0.2.10 dst=203.0.113.9 sport=54321 dport=443 src=203.0.113.9 dst=192.0.2.10 sport=443 dport=54321 [ASSURED] mark=0 use=2"
	targets := []netip.Prefix{netip.MustParsePrefix("192.0.2.10/32")}
	session, ok := parseConntrackLine(line, targets)
	if !ok {
		t.Fatal("expected target session")
	}
	if session.Network != "tcp" || session.State != "ESTABLISHED" {
		t.Fatalf("unexpected protocol state: %#v", session)
	}
	if session.SourcePort != "54321" || session.DestinationPort != "443" {
		t.Fatalf("unexpected ports: %#v", session)
	}
	if session.ID == "" {
		t.Fatal("expected stable session id")
	}
}

func TestParseConntrackLineRejectsOtherDevice(t *testing.T) {
	line := "ipv4 2 udp 17 20 src=192.0.2.50 dst=1.1.1.1 sport=45000 dport=53 src=1.1.1.1 dst=192.0.2.50 sport=53 dport=45000 mark=0 use=2"
	targets := []netip.Prefix{netip.MustParsePrefix("192.0.2.10/32")}
	if _, ok := parseConntrackLine(line, targets); ok {
		t.Fatal("unexpected session for non-target device")
	}
}

func TestFlexibleString(t *testing.T) {
	var value struct {
		Text   flexibleString `json:"text"`
		Number flexibleString `json:"number"`
	}
	if err := json.Unmarshal([]byte(`{"text":"443","number":7890}`), &value); err != nil {
		t.Fatal(err)
	}
	if value.Text != "443" || value.Number != "7890" {
		t.Fatalf("unexpected values: %#v", value)
	}
}

func TestActionTargets(t *testing.T) {
	cases := map[string]string{
		"start_shellcrash": "shellcrash",
		"restart_leigod":   "leigod",
		"stop_all":         "off",
	}
	for action, expected := range cases {
		target, _, err := actionTarget(action)
		if err != nil || target != expected {
			t.Fatalf("%s: target=%s err=%v", action, target, err)
		}
	}
	if _, _, err := actionTarget("shell"); err == nil {
		t.Fatal("expected unknown action to fail")
	}
}
