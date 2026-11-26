package netfix

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// echConfig builds an ECHConfig (draft-ietf-tls-esni, version 0xfe0d) for X25519 / HKDF-SHA256 /
// AES-128-GCM, and the ECHConfigList holding just it.
func echConfig(t *testing.T, id byte, publicName string) (cfg, list, priv []byte) {
	t.Helper()
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var c []byte
	c = append(c, id)
	c = binary.BigEndian.AppendUint16(c, 0x0020) // DHKEM(X25519, HKDF-SHA256)
	pub := k.PublicKey().Bytes()
	c = binary.BigEndian.AppendUint16(c, uint16(len(pub)))
	c = append(c, pub...)
	c = binary.BigEndian.AppendUint16(c, 4)
	c = binary.BigEndian.AppendUint16(c, 0x0001) // HKDF-SHA256
	c = binary.BigEndian.AppendUint16(c, 0x0001) // AES-128-GCM
	c = append(c, 0)                             // maximum_name_length
	c = append(c, byte(len(publicName)))
	c = append(c, publicName...)
	c = binary.BigEndian.AppendUint16(c, 0) // extensions
	cfg = binary.BigEndian.AppendUint16(nil, 0xfe0d)
	cfg = binary.BigEndian.AppendUint16(cfg, uint16(len(c)))
	cfg = append(cfg, c...)
	list = binary.BigEndian.AppendUint16(nil, uint16(len(cfg)))
	list = append(list, cfg...)
	return cfg, list, k.Bytes()
}

// sniFilter sits in front of addr and answers with a block page whenever the handshake shows
// example.com in the clear; anything else is passed through.
func sniFilter(t *testing.T, addr string) (string, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var blocked atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				// the ClientHello: a 5-byte record header, then the record
				hdr := make([]byte, 5)
				if _, err := io.ReadFull(c, hdr); err != nil {
					return
				}
				body := make([]byte, binary.BigEndian.Uint16(hdr[3:]))
				if _, err := io.ReadFull(c, body); err != nil {
					return
				}
				hello := append(hdr, body...)
				clear := bytes.ReplaceAll(hello, []byte("public.example.com"), nil)
				if bytes.Contains(clear, []byte("example.com")) {
					blocked.Add(1)
					io.WriteString(c, "HTTP/1.1 403 Forbidden\r\nContent-Length: 7\r\n\r\nblocked")
					return
				}
				up, err := net.Dial("tcp", addr)
				if err != nil {
					return
				}
				defer up.Close()
				up.Write(hello)
				go io.Copy(up, c)
				io.Copy(c, up)
			}()
		}
	}()
	return ln.Addr().String(), &blocked
}

// a filter that reads the name in the handshake (not DNS): the fallback gets through with ECH,
// including when the ECH key it starts with is out of date
func TestFallbackHidesNameWithECH(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "current key", true: "stale key"}[stale], func(t *testing.T) {
			cfg, list, priv := echConfig(t, 1, "public.example.com")
			real := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, "dashboard")
			}))
			real.TLS = &tls.Config{EncryptedClientHelloKeys: []tls.EncryptedClientHelloKey{
				{Config: cfg, PrivateKey: priv, SendAsRetry: true}}}
			real.StartTLS()
			t.Cleanup(real.Close)
			faddr, blocked := sniFilter(t, strings.TrimPrefix(real.URL, "https://"))
			_, port, _ := net.SplitHostPort(faddr)

			clientList := list
			if stale {
				_, clientList, _ = echConfig(t, 2, "public.example.com")
			}
			tlsCfg := real.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
			// DNS is honest here: example.com is the filter's address
			base := &http.Transport{TLSClientConfig: tlsCfg,
				DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, network, faddr)
				}}
			f := newFallbackTransportWith(base, tlsCfg, func(context.Context, string) (route, error) {
				return route{ips: []string{"127.0.0.1"}, ech: clientList}, nil
			})
			f.Allow = func(string) bool { return true }
			var usedECH bool
			f.OnFallback = func(_ string, _ []string, ech bool) { usedECH = ech }
			c := &http.Client{Transport: f}
			for range 2 {
				res, err := c.Get("https://example.com:" + port + "/api/x")
				if err != nil {
					t.Fatal(err)
				}
				b, _ := io.ReadAll(res.Body)
				res.Body.Close()
				if string(b) != "dashboard" {
					t.Fatalf("got %q", b)
				}
			}
			if !usedECH || blocked.Load() != 1 {
				t.Fatalf("ech %v, blocked %d (want only the first, normal attempt blocked)", usedECH, blocked.Load())
			}
		})
	}
}

func TestECHFromHTTPSRecord(t *testing.T) {
	rec := `1 . alpn=h3,h2 ipv4hint=104.21.49.166 ech=` + echSeed["hotsdash.com"] + ` ipv6hint=2606:4700::1`
	got := echFromHTTPS([]string{"garbage", rec})
	if len(got) == 0 || !bytes.Equal(got, seedECH("hotsdash.com")) {
		t.Fatalf("got %x", got)
	}
	if echFromHTTPS([]string{"1 . alpn=h2"}) != nil {
		t.Fatal("no ech= should give nil")
	}
}
