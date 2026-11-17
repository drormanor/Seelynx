// Command metadata-probe reads a camera's RTSP metadata track directly from the camera
// and saves what arrives. It reads the login from the database, so no password is typed
// or shown. Development tool.
//
//	metadata-probe -data data -camera 103 -path stream3 -seconds 120 -out meta.xml
package main

import (
	"bufio"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"seelynx/internal/store"
)

func main() {
	data := flag.String("data", "data", "data directory")
	id := flag.Int64("camera", 0, "camera ID")
	path := flag.String("path", "stream3", "RTSP path on the camera")
	seconds := flag.Int("seconds", 60, "how long to listen")
	out := flag.String("out", "metadata.bin", "file for the payloads")
	flag.Parse()
	if err := run(*data, *id, *path, *seconds, *out); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type client struct {
	conn       net.Conn
	r          *bufio.Reader
	cseq       int
	user, pass string
	realm      string
	nonce      string
	basic      bool
}

func md5hex(s string) string {
	h := md5.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

func (c *client) authHeader(method, uri string) string {
	if c.basic {
		return "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(c.user+":"+c.pass)) + "\r\n"
	}
	if c.nonce == "" {
		return ""
	}
	resp := md5hex(md5hex(c.user+":"+c.realm+":"+c.pass) + ":" + c.nonce + ":" + md5hex(method+":"+uri))
	return fmt.Sprintf("Authorization: Digest username=%q, realm=%q, nonce=%q, uri=%q, response=%q\r\n", c.user, c.realm, c.nonce, uri, resp)
}

var (
	realmRe = regexp.MustCompile(`realm="([^"]*)"`)
	nonceRe = regexp.MustCompile(`nonce="([^"]*)"`)
)

func (c *client) readResponse() (int, map[string]string, string, error) {
	line, err := c.r.ReadString('\n')
	if err != nil {
		return 0, nil, "", err
	}
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return 0, nil, "", fmt.Errorf("bad status line")
	}
	code, _ := strconv.Atoi(parts[1])
	head := map[string]string{}
	for {
		l, err := c.r.ReadString('\n')
		if err != nil {
			return 0, nil, "", err
		}
		l = strings.TrimSpace(l)
		if l == "" {
			break
		}
		if k, v, ok := strings.Cut(l, ":"); ok {
			key := strings.ToLower(strings.TrimSpace(k))
			if key == "www-authenticate" && head[key] != "" && !strings.HasPrefix(strings.TrimSpace(v), "Digest") {
				continue // prefer the Digest challenge when both are offered
			}
			head[key] = strings.TrimSpace(v)
		}
	}
	body := ""
	if n, _ := strconv.Atoi(head["content-length"]); n > 0 {
		b := make([]byte, n)
		if _, err := io.ReadFull(c.r, b); err != nil {
			return 0, nil, "", err
		}
		body = string(b)
	}
	return code, head, body, nil
}

func (c *client) do(method, uri, extra string) (int, map[string]string, string, error) {
	for attempt := 0; ; attempt++ {
		c.cseq++
		req := fmt.Sprintf("%s %s RTSP/1.0\r\nCSeq: %d\r\nUser-Agent: seelynx-probe\r\n%s%s\r\n", method, uri, c.cseq, c.authHeader(method, uri), extra)
		if _, err := c.conn.Write([]byte(req)); err != nil {
			return 0, nil, "", err
		}
		code, head, body, err := c.readResponse()
		if err != nil {
			return 0, nil, "", err
		}
		if code != 401 || attempt > 0 {
			return code, head, body, nil
		}
		ch := head["www-authenticate"]
		if m := nonceRe.FindStringSubmatch(ch); m != nil {
			c.nonce = m[1]
			if r := realmRe.FindStringSubmatch(ch); r != nil {
				c.realm = r[1]
			}
		} else {
			c.basic = true
		}
	}
}

func run(dataDir string, id int64, path string, seconds int, out string) error {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(dataDir, "seelynx.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	key, err := store.LoadOrCreateKey(filepath.Join(dataDir, "secret.key"))
	if err != nil {
		return err
	}
	if _, err := st.UseKey(ctx, key); err != nil {
		return err
	}
	cam, err := st.Camera(ctx, id)
	if err != nil {
		return err
	}
	u, err := url.Parse(cam.MainURL)
	if err != nil {
		return err
	}
	host := u.Host
	if u.Port() == "" {
		host += ":554"
	}
	conn, err := net.DialTimeout("tcp", host, 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	c := &client{conn: conn, r: bufio.NewReaderSize(conn, 1<<20), user: cam.Username, pass: cam.Password}
	uri := "rtsp://" + host + "/" + path

	code, _, sdp, err := c.do("DESCRIBE", uri, "Accept: application/sdp\r\n")
	if err != nil {
		return err
	}
	fmt.Println("DESCRIBE", code)
	for _, l := range strings.Split(sdp, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "m=") || strings.HasPrefix(l, "a=rtpmap") || strings.HasPrefix(l, "a=control") {
			fmt.Println(" ", l)
		}
	}
	// Set up every track that isn't video or audio, each on its own channel.
	session := ""
	names := map[byte]string{}
	files := map[byte]*os.File{}
	channel := byte(0)
	for _, section := range strings.Split(sdp, "m=")[1:] {
		kind, _, _ := strings.Cut(section, " ")
		if kind == "video" || kind == "audio" {
			continue
		}
		m := regexp.MustCompile(`a=control:(\S+)`).FindStringSubmatch(section)
		if m == nil {
			continue
		}
		control := m[1]
		if !strings.HasPrefix(control, "rtsp") {
			control = strings.TrimRight(uri, "/") + "/" + control
		}
		extra := fmt.Sprintf("Transport: RTP/AVP/TCP;unicast;interleaved=%d-%d\r\n", channel, channel+1)
		if session != "" {
			extra += "Session: " + session + "\r\n"
		}
		code, head, _, err := c.do("SETUP", control, extra)
		if err != nil {
			return err
		}
		fmt.Println("SETUP", kind, code)
		if code != 200 {
			continue
		}
		if s, _, _ := strings.Cut(head["session"], ";"); s != "" {
			session = s
		}
		f, err := os.Create(out + "." + kind)
		if err != nil {
			return err
		}
		defer f.Close()
		names[channel], files[channel] = kind, f
		channel += 2
	}
	if len(files) == 0 {
		return fmt.Errorf("no metadata track in this stream")
	}
	code, _, _, err = c.do("PLAY", uri, "Session: "+session+"\r\nRange: npt=0.000-\r\n")
	if err != nil {
		return err
	}
	fmt.Println("PLAY", code)

	end := time.Now().Add(time.Duration(seconds) * time.Second)
	packets, bytes := map[string]int{}, map[string]int{}
	for time.Now().Before(end) {
		conn.SetReadDeadline(end)
		b, err := c.r.ReadByte()
		if err != nil {
			break
		}
		if b != '$' {
			continue
		}
		hdr := make([]byte, 3)
		if _, err := io.ReadFull(c.r, hdr); err != nil {
			break
		}
		p := make([]byte, binary.BigEndian.Uint16(hdr[1:]))
		if _, err := io.ReadFull(c.r, p); err != nil {
			break
		}
		if hdr[0]%2 != 0 || len(p) < 12 {
			continue // RTCP
		}
		o := 12 + int(p[0]&15)*4
		if p[0]&16 != 0 && len(p) >= o+4 {
			o += 4 + int(binary.BigEndian.Uint16(p[o+2:]))*4
		}
		if o > len(p) {
			continue
		}
		f := files[hdr[0]]
		if f == nil {
			continue
		}
		packets[names[hdr[0]]]++
		bytes[names[hdr[0]]] += len(p) - o
		f.Write(p[o:])
	}
	for _, kind := range names {
		fmt.Println(kind, "packets", packets[kind], "bytes", bytes[kind])
	}
	return nil
}
