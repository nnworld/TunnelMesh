package protocol

import "testing"

func TestClientSubprotocolsArePriorityOrdered(t *testing.T) {
	got := ClientSubprotocols()
	want := []string{
		"tunnelmesh.v1.open-result.flow-control.metadata",
		"tunnelmesh.v1.open-result.flow-control",
		"tunnelmesh.v1.open-result",
		"tunnelmesh.v1",
	}
	if len(got) != len(want) {
		t.Fatalf("subprotocols = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("subprotocols = %v, want %v", got, want)
		}
	}
}

func TestClientSubprotocolsPreferMetadata(t *testing.T) {
	selected, ok := SelectSubprotocol(ClientSubprotocols())
	if !ok || selected != SubprotocolClientMetadata {
		t.Fatalf("selected = %q, ok=%v", selected, ok)
	}
}

func TestSelectSubprotocol(t *testing.T) {
	tests := []struct {
		name    string
		offered []string
		want    string
		modern  bool
	}{
		{name: "modern strict and flow control", offered: []string{"other", "tunnelmesh.v1.open-result.flow-control", "tunnelmesh.v1.open-result", "tunnelmesh.v1"}, want: "tunnelmesh.v1.open-result.flow-control", modern: true},
		{name: "modern strict only", offered: []string{"tunnelmesh.v1.open-result", "tunnelmesh.v1"}, want: "tunnelmesh.v1.open-result", modern: true},
		{name: "comma separated header", offered: []string{"other, tunnelmesh.v1.open-result.flow-control, tunnelmesh.v1.open-result, tunnelmesh.v1"}, want: "tunnelmesh.v1.open-result.flow-control", modern: true},
		{name: "legacy", offered: []string{"tunnelmesh.v1"}, want: "tunnelmesh.v1"},
		{name: "missing", offered: nil, want: ""},
		{name: "unsupported", offered: []string{"other"}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, modern := SelectSubprotocol(tt.offered)
			if got != tt.want || modern != tt.modern {
				t.Fatalf("SelectSubprotocol() = (%q, %v), want (%q, %v)", got, modern, tt.want, tt.modern)
			}
		})
	}
}
