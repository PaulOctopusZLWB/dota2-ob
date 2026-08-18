package m4match

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"time"
)

const obsWebSocketPort = 4455

func stopOBSRecording(ctx context.Context, root string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	dialer := net.Dialer{Timeout: 500 * time.Millisecond}
	var connection net.Conn
	var err error
	for time.Now().Before(deadline) {
		connection, err = dialer.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", obsWebSocketPort))
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return errors.New("owned OBS websocket unavailable")
	}
	defer connection.Close()
	_ = connection.SetDeadline(deadline)
	reader := bufio.NewReader(connection)
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		return err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	request := fmt.Sprintf("GET / HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", obsWebSocketPort, key)
	if _, err := io.WriteString(connection, request); err != nil {
		return err
	}
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		return err
	}
	wantAccept := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	if response.StatusCode != http.StatusSwitchingProtocols || response.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(wantAccept[:]) {
		return errors.New("owned OBS websocket upgrade rejected")
	}
	if _, err := readOBSWebSocketJSON(reader); err != nil { // Hello
		return err
	}
	if err := writeOBSWebSocketJSON(connection, map[string]any{"op": 1, "d": map[string]any{"rpcVersion": 1}}); err != nil {
		return err
	}
	for {
		message, err := readOBSWebSocketJSON(reader)
		if err != nil {
			return err
		}
		op, ok := message["op"].(float64)
		if !ok {
			return errors.New("owned OBS websocket opcode invalid")
		}
		if int(op) == 2 {
			break
		}
	}
	requestID := "dot87-stop-recording"
	if err := writeOBSWebSocketJSON(connection, map[string]any{"op": 6, "d": map[string]any{"requestType": "StopRecord", "requestId": requestID}}); err != nil {
		return err
	}
	for {
		message, err := readOBSWebSocketJSON(reader)
		if err != nil {
			return err
		}
		op, _ := message["op"].(float64)
		data, _ := message["d"].(map[string]any)
		if int(op) != 7 || data["requestId"] != requestID {
			continue
		}
		status, _ := data["requestStatus"].(map[string]any)
		result, _ := status["result"].(bool)
		if !result {
			return errors.New("owned OBS refused StopRecord")
		}
		break
	}
	for time.Now().Before(deadline) {
		if recordingFinalized(filepath.Join(root, "recordings"), filepath.Join(root, "evidence/logs/obs-live.log")) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("owned OBS recording did not finalize before shutdown")
}

func readOBSWebSocketJSON(reader *bufio.Reader) (map[string]any, error) {
	first, err := reader.ReadByte()
	if err != nil {
		return nil, err
	}
	second, err := reader.ReadByte()
	if err != nil {
		return nil, err
	}
	if first&0x0f != 1 || second&0x80 != 0 {
		return nil, errors.New("unexpected owned OBS websocket frame")
	}
	length := uint64(second & 0x7f)
	switch length {
	case 126:
		var encoded [2]byte
		if _, err := io.ReadFull(reader, encoded[:]); err != nil {
			return nil, err
		}
		length = uint64(binary.BigEndian.Uint16(encoded[:]))
	case 127:
		var encoded [8]byte
		if _, err := io.ReadFull(reader, encoded[:]); err != nil {
			return nil, err
		}
		length = binary.BigEndian.Uint64(encoded[:])
	}
	if length > 1<<20 {
		return nil, errors.New("owned OBS websocket frame exceeds bound")
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	var message map[string]any
	if json.Unmarshal(payload, &message) != nil {
		return nil, errors.New("owned OBS websocket JSON invalid")
	}
	return message, nil
}

func writeOBSWebSocketJSON(connection net.Conn, value any) error {
	payload, err := json.Marshal(value)
	if err != nil || len(payload) > 1<<20 {
		return errors.New("owned OBS websocket request invalid")
	}
	mask := make([]byte, 4)
	if _, err := rand.Read(mask); err != nil {
		return err
	}
	header := []byte{0x81}
	switch {
	case len(payload) < 126:
		header = append(header, 0x80|byte(len(payload)))
	case len(payload) <= 65535:
		header = append(header, 0x80|126, byte(len(payload)>>8), byte(len(payload)))
	default:
		header = append(header, 0x80|127, 0, 0, 0, 0, byte(len(payload)>>24), byte(len(payload)>>16), byte(len(payload)>>8), byte(len(payload)))
	}
	header = append(header, mask...)
	masked := append([]byte(nil), payload...)
	for index := range masked {
		masked[index] ^= mask[index%4]
	}
	_, err = connection.Write(append(header, masked...))
	return err
}
