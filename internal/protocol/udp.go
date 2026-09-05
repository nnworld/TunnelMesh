package protocol

import "fmt"

const MaxDatagram = 64 * 1024

// UDPAssociation keeps datagram boundaries intact. No framing bytes are added:
// each association call maps one WebSocket binary message to one datagram.
type UDPAssociation struct{ maxDatagram int }

func NewUDPAssociation(maxDatagram int) *UDPAssociation {
	if maxDatagram <= 0 || maxDatagram > MaxDatagram {
		maxDatagram = MaxDatagram
	}
	return &UDPAssociation{maxDatagram: maxDatagram}
}

func (a *UDPAssociation) EncodeDatagram(payload []byte) ([]byte, error) {
	if a == nil {
		return nil, fmt.Errorf("protocol: nil UDP association")
	}
	if len(payload) > a.maxDatagram {
		return nil, ErrDatagramTooLarge
	}
	return append([]byte(nil), payload...), nil
}

func (a *UDPAssociation) DecodeDatagram(payload []byte) ([]byte, error) {
	if a == nil {
		return nil, fmt.Errorf("protocol: nil UDP association")
	}
	if len(payload) > a.maxDatagram {
		return nil, ErrDatagramTooLarge
	}
	return append([]byte(nil), payload...), nil
}

func (a *UDPAssociation) WriteDatagram(payload []byte) ([]byte, error) {
	return a.EncodeDatagram(payload)
}

func (a *UDPAssociation) ReadDatagram(payload []byte) ([]byte, error) {
	return a.DecodeDatagram(payload)
}

func (a *UDPAssociation) MaxDatagram() int {
	if a == nil {
		return 0
	}
	return a.maxDatagram
}
