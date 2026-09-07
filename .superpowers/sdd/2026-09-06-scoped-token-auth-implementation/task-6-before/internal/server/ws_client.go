package server

// Client WebSocket handling shares the versioned frame transport with agents.
// Keeping this adapter separate makes authentication and routing handlers easy
// to evolve without coupling them to a WebSocket implementation.
type ClientWSHandler struct{ Sessions *ClientSessionManager }

func (h *ClientWSHandler) Attach(id string, c WSConn) *WSFrameTransport {
	tr := NewWSFrameTransport(c)
	if h != nil && h.Sessions != nil {
		h.Sessions.Register(id, tr)
	}
	return tr
}
