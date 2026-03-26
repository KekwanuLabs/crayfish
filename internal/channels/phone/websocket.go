package phone

// Minimal WebSocket server implementation — no external dependencies.
// Only implements what ConversationRelay needs:
//   - HTTP upgrade handshake
//   - Read masked text frames (client→server are always masked per RFC 6455)
//   - Write unmasked text frames (server→client are never masked)
//   - Graceful close

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// WSConn is a minimal server-side WebSocket connection.
type WSConn struct {
	conn   net.Conn
	reader *bufio.Reader
	mu     sync.Mutex // protects writes
}

// UpgradeWS upgrades an HTTP connection to WebSocket.
func UpgradeWS(w http.ResponseWriter, r *http.Request) (*WSConn, error) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "not a websocket upgrade", http.StatusBadRequest)
		return nil, fmt.Errorf("not a WebSocket upgrade")
	}

	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "missing Sec-WebSocket-Key", http.StatusBadRequest)
		return nil, fmt.Errorf("missing Sec-WebSocket-Key")
	}

	// Compute the accept key per RFC 6455 §1.3.
	h := sha1.New()
	h.Write([]byte(key + wsGUID))
	accept := base64.StdEncoding.EncodeToString(h.Sum(nil))

	// Hijack the raw TCP connection.
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return nil, fmt.Errorf("hijack not supported")
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, fmt.Errorf("hijack: %w", err)
	}

	// Send 101 Switching Protocols.
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := rw.WriteString(resp); err != nil {
		conn.Close()
		return nil, fmt.Errorf("write upgrade: %w", err)
	}
	if err := rw.Flush(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("flush upgrade: %w", err)
	}

	return &WSConn{conn: conn, reader: rw.Reader}, nil
}

// WebSocket opcodes.
const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

// ReadJSON reads the next text frame and JSON-decodes it into v.
func (ws *WSConn) ReadJSON(v interface{}) error {
	for {
		data, opcode, err := ws.readFrame()
		if err != nil {
			return err
		}
		switch opcode {
		case opText:
			return json.Unmarshal(data, v)
		case opBinary:
			return json.Unmarshal(data, v) // treat binary as JSON too
		case opClose:
			ws.sendClose()
			return fmt.Errorf("websocket closed by peer")
		case opPing:
			ws.sendPong(data)
		// opPong, opContinuation: ignore
		}
	}
}

// WriteJSON JSON-encodes v and sends it as a text frame.
func (ws *WSConn) WriteJSON(v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return ws.writeFrame(opText, data)
}

// Close sends a close frame and closes the underlying connection.
func (ws *WSConn) Close() {
	ws.sendClose()
	ws.conn.Close()
}

// readFrame reads one complete WebSocket frame, handling masking.
func (ws *WSConn) readFrame() (payload []byte, opcode byte, err error) {
	// Read 2-byte header.
	header := make([]byte, 2)
	if _, err := ws.reader.Read(header[:1]); err != nil {
		return nil, 0, err
	}
	if _, err := ws.reader.Read(header[1:]); err != nil {
		return nil, 0, err
	}

	// fin := (header[0] & 0x80) != 0  // we don't need FIN for simple frames
	opcode = header[0] & 0x0F
	masked := (header[1] & 0x80) != 0
	payloadLen := int64(header[1] & 0x7F)

	switch payloadLen {
	case 126:
		var ext uint16
		if err := binary.Read(ws.reader, binary.BigEndian, &ext); err != nil {
			return nil, opcode, err
		}
		payloadLen = int64(ext)
	case 127:
		var ext uint64
		if err := binary.Read(ws.reader, binary.BigEndian, &ext); err != nil {
			return nil, opcode, err
		}
		payloadLen = int64(ext)
	}

	// Read masking key (always present for client→server frames).
	var maskKey [4]byte
	if masked {
		if _, err := ws.reader.Read(maskKey[:]); err != nil {
			return nil, opcode, err
		}
	}

	// Read payload.
	payload = make([]byte, payloadLen)
	if _, err := ws.reader.Read(payload); err != nil {
		return nil, opcode, err
	}

	// Unmask.
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}

	return payload, opcode, nil
}

// writeFrame sends a single WebSocket frame (server→client, never masked).
func (ws *WSConn) writeFrame(opcode byte, payload []byte) error {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	n := len(payload)
	var header []byte

	// FIN=1, RSV=0, opcode.
	b0 := byte(0x80) | opcode

	switch {
	case n <= 125:
		header = []byte{b0, byte(n)}
	case n <= 65535:
		header = make([]byte, 4)
		header[0] = b0
		header[1] = 126
		binary.BigEndian.PutUint16(header[2:], uint16(n))
	default:
		header = make([]byte, 10)
		header[0] = b0
		header[1] = 127
		binary.BigEndian.PutUint64(header[2:], uint64(n))
	}

	if _, err := ws.conn.Write(header); err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err := ws.conn.Write(payload)
		return err
	}
	return nil
}

func (ws *WSConn) sendClose() {
	_ = ws.writeFrame(opClose, []byte{0x03, 0xE8}) // 1000 = normal closure
}

func (ws *WSConn) sendPong(data []byte) {
	_ = ws.writeFrame(opPong, data)
}
