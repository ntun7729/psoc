package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestProtocolChecks(t *testing.T) {
	cases := []struct {
		name    string
		handler func(net.Conn) error
	}{
		{"http", fakeHTTPProxy},
		{"socks5", fakeSOCKS5Proxy},
		{"socks4", fakeSOCKS4Proxy},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addr, stop := startFakeProxy(t, tc.handler)
			defer stop()
			s, err := NewScanner(Config{Timeout: 2 * time.Second, VerifyURL: "http://example.com/", AllowPrivate: true})
			if err != nil {
				t.Fatal(err)
			}
			status, err := s.checkProtocol(context.Background(), addr, tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if status != http.StatusNoContent {
				t.Fatalf("status=%d", status)
			}
		})
	}
}

func startFakeProxy(t *testing.T, handler func(net.Conn) error) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		if err := handler(c); err != nil {
			t.Logf("fake proxy: %v", err)
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close(); <-done }
}

func fakeHTTPProxy(c net.Conn) error {
	br := bufio.NewReader(c)
	line, err := br.ReadString('\n')
	if err != nil {
		return err
	}
	if !strings.HasPrefix(line, "GET http://example.com/") {
		return fmt.Errorf("unexpected request line %q", line)
	}
	if err := readHeaders(br); err != nil {
		return err
	}
	_, err = io.WriteString(c, "HTTP/1.1 204 No Content\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
	return err
}

func fakeSOCKS5Proxy(c net.Conn) error {
	var hello [3]byte
	if _, err := io.ReadFull(c, hello[:]); err != nil {
		return err
	}
	if hello != [3]byte{0x05, 0x01, 0x00} {
		return fmt.Errorf("bad hello %x", hello)
	}
	if _, err := c.Write([]byte{0x05, 0x00}); err != nil {
		return err
	}
	head := make([]byte, 4)
	if _, err := io.ReadFull(c, head); err != nil {
		return err
	}
	if head[0] != 0x05 || head[1] != 0x01 {
		return fmt.Errorf("bad connect header %x", head)
	}
	switch head[3] {
	case 0x01:
		if _, err := io.CopyN(io.Discard, c, 4+2); err != nil {
			return err
		}
	case 0x04:
		if _, err := io.CopyN(io.Discard, c, 16+2); err != nil {
			return err
		}
	case 0x03:
		var n [1]byte
		if _, err := io.ReadFull(c, n[:]); err != nil {
			return err
		}
		if _, err := io.CopyN(io.Discard, c, int64(n[0])+2); err != nil {
			return err
		}
	default:
		return fmt.Errorf("bad atyp")
	}
	if _, err := c.Write([]byte{0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, 0, 80}); err != nil {
		return err
	}
	return readGETAndReply204(c)
}

func fakeSOCKS4Proxy(c net.Conn) error {
	head := make([]byte, 8)
	if _, err := io.ReadFull(c, head); err != nil {
		return err
	}
	if head[0] != 0x04 || head[1] != 0x01 {
		return fmt.Errorf("bad socks4 header %x", head[:2])
	}
	br := bufio.NewReader(c)
	if _, err := br.ReadString(0); err != nil {
		return err
	}
	if head[4] == 0 && head[5] == 0 && head[6] == 0 && head[7] != 0 {
		if _, err := br.ReadString(0); err != nil {
			return err
		}
	}
	reply := make([]byte, 8)
	reply[1] = 0x5a
	binary.BigEndian.PutUint16(reply[2:4], 80)
	copy(reply[4:], []byte{127, 0, 0, 1})
	if _, err := c.Write(reply); err != nil {
		return err
	}
	return readGETAndReply204WithReader(c, br)
}

func readGETAndReply204(c net.Conn) error { return readGETAndReply204WithReader(c, bufio.NewReader(c)) }
func readGETAndReply204WithReader(c net.Conn, br *bufio.Reader) error {
	line, err := br.ReadString('\n')
	if err != nil {
		return err
	}
	if !strings.HasPrefix(line, "GET / HTTP/1.1") {
		return fmt.Errorf("unexpected GET %q", line)
	}
	if err := readHeaders(br); err != nil {
		return err
	}
	_, err = io.WriteString(c, "HTTP/1.1 204 No Content\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
	return err
}

func readHeaders(br *bufio.Reader) error {
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return err
		}
		if line == "\r\n" {
			return nil
		}
	}
}
