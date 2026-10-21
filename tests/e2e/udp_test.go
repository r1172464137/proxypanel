package e2e

import (
	"encoding/binary"
	"encoding/hex"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// openVLESSUDP verifies a real UDP datagram forwarded by the agent-managed
// Xray core, then leaves its VLESS UDP association open for hard revocation.
func openVLESSUDP(t *testing.T, inbound int, outbound map[string]any) net.Conn {
	t.Helper()
	settings := outbound["settings"].(map[string]any)
	server := settings["vnext"].([]any)[0].(map[string]any)
	user := server["users"].([]any)[0].(map[string]any)
	uuid, err := hex.DecodeString(strings.ReplaceAll(user["id"].(string), "-", ""))
	if err != nil || len(uuid) != 16 {
		t.Fatal("invalid exported VLESS UUID")
	}
	echo, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { echo.Close() })
	go func() {
		buf := make([]byte, 65535)
		for {
			n, addr, err := echo.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = echo.WriteTo(buf[:n], addr)
		}
	}()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(inbound)), 3*time.Second)
	if err != nil {
		t.Fatal("open UDP association failed")
	}
	t.Cleanup(func() { conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	header := append([]byte{0}, uuid...)
	header = append(header, 0, 2) // no addons, UDP command
	header = binary.BigEndian.AppendUint16(header, uint16(echo.LocalAddr().(*net.UDPAddr).Port))
	header = append(header, 1, 127, 0, 0, 1)
	payload := []byte("real-udp-revocation-probe")
	header = binary.BigEndian.AppendUint16(header, uint16(len(payload)))
	header = append(header, payload...)
	if _, err = conn.Write(header); err != nil {
		t.Fatal("write VLESS UDP request failed")
	}
	response := make([]byte, 2)
	if _, err = io.ReadFull(conn, response); err != nil {
		t.Fatal("UDP association response failed")
	}
	if response[0] != 0 || response[1] != 0 {
		t.Fatal("unexpected VLESS response header")
	}
	if _, err = io.ReadFull(conn, response); err != nil {
		t.Fatal("UDP packet length missing")
	}
	body := make([]byte, binary.BigEndian.Uint16(response))
	if _, err = io.ReadFull(conn, body); err != nil || string(body) != string(payload) {
		t.Fatal("UDP echo payload mismatch")
	}
	_ = conn.SetDeadline(time.Time{})
	t.Logf("real Xray UDP forwarding verified (%d bytes)", len(body))
	return conn
}

func assertUDPRevoked(t *testing.T, conn net.Conn, revoked time.Time) {
	t.Helper()
	_ = conn.SetReadDeadline(revoked.Add(60 * time.Second))
	var one [1]byte
	n, err := conn.Read(one[:])
	if n != 0 || err == nil {
		t.Fatal("revoked UDP association still returned data")
	}
	if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("UDP association survived 60 seconds")
	}
	t.Logf("existing UDP association terminated within %.3fs after batch revocation", time.Since(revoked).Seconds())
}
