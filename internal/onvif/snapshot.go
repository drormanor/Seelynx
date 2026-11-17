package onvif

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// MaxSnapshot is the largest picture accepted from a camera.
const MaxSnapshot = 5 << 20

// SnapshotURI asks the camera where to fetch a picture of its largest profile.
func (c *Client) SnapshotURI(ctx context.Context) (string, error) {
	var caps struct {
		XAddr string `xml:"Body>GetCapabilitiesResponse>Capabilities>Media>XAddr"`
	}
	err := c.call(ctx, c.XAddr, `<GetCapabilities xmlns="http://www.onvif.org/ver10/device/wsdl"><Category>Media</Category></GetCapabilities>`, &caps)
	if err != nil {
		return "", err
	}
	if caps.XAddr == "" {
		return "", errors.New("camera reported no media service")
	}
	var profiles struct {
		Items []struct {
			Token  string `xml:"token,attr"`
			Width  int    `xml:"VideoEncoderConfiguration>Resolution>Width"`
			Height int    `xml:"VideoEncoderConfiguration>Resolution>Height"`
		} `xml:"Body>GetProfilesResponse>Profiles"`
	}
	if err := c.call(ctx, caps.XAddr, `<GetProfiles xmlns="http://www.onvif.org/ver10/media/wsdl"/>`, &profiles); err != nil {
		return "", err
	}
	if len(profiles.Items) == 0 {
		return "", errors.New("camera reported no video profiles")
	}
	best := profiles.Items[0]
	for _, p := range profiles.Items {
		if p.Width*p.Height > best.Width*best.Height {
			best = p
		}
	}
	var esc bytes.Buffer
	xml.EscapeText(&esc, []byte(best.Token))
	var uri struct {
		URI string `xml:"Body>GetSnapshotUriResponse>MediaUri>Uri"`
	}
	if err := c.call(ctx, caps.XAddr, `<GetSnapshotUri xmlns="http://www.onvif.org/ver10/media/wsdl"><ProfileToken>`+esc.String()+`</ProfileToken></GetSnapshotUri>`, &uri); err != nil {
		return "", err
	}
	if uri.URI == "" {
		return "", errors.New("camera doesn't offer snapshots")
	}
	return uri.URI, nil
}

// Snapshot fetches a JPEG picture from the camera.
func (c *Client) Snapshot(ctx context.Context) ([]byte, error) {
	uri, err := c.SnapshotURI(ctx)
	if err != nil {
		return nil, err
	}
	return c.Fetch(ctx, uri)
}

// Fetch reads a JPEG from a snapshot address, answering the camera's login challenge.
// Fetch reads a JPEG from a snapshot address, answering the camera's login challenge.
func (c *Client) Fetch(ctx context.Context, uri string) ([]byte, error) {
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{}
	}
	get := func(auth string, basic bool) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
		if err != nil {
			return nil, errors.New("camera gave an invalid snapshot address")
		}
		if basic {
			req.SetBasicAuth(c.User, c.Pass)
		} else if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		res, err := hc.Do(req)
		var ue *url.Error
		if errors.As(err, &ue) {
			return nil, ue.Err // the address can carry a login
		}
		return res, err
	}
	res, err := get("", false)
	if err != nil {
		return nil, err
	}
	defer func() { res.Body.Close() }()
	if res.StatusCode == http.StatusUnauthorized {
		challenge := res.Header.Get("WWW-Authenticate")
		io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
		res.Body.Close()
		if strings.HasPrefix(strings.ToLower(challenge), "digest") {
			res, err = get(digestAuth(c.User, c.Pass, uri, challenge), false)
		} else {
			res, err = get("", true)
		}
		if err != nil {
			return nil, err
		}
	}
	if res.StatusCode == http.StatusUnauthorized {
		return nil, ErrUnauthorized
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("camera returned HTTP %d for its snapshot", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, MaxSnapshot+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxSnapshot {
		return nil, errors.New("the camera's snapshot is too large")
	}
	if len(data) < 3 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil, errors.New("the camera's snapshot isn't a JPEG picture")
	}
	return data, nil
}

var digestField = regexp.MustCompile(`(\w+)=(?:"([^"]*)"|([^\s,]+))`)

// digestAuth answers an HTTP digest challenge with MD5, which is what cameras use.
func digestAuth(user, pass, uri, challenge string) string {
	f := map[string]string{}
	for _, m := range digestField.FindAllStringSubmatch(challenge, -1) {
		f[strings.ToLower(m[1])] = m[2] + m[3]
	}
	path := uri
	if u, err := url.Parse(uri); err == nil {
		path = u.RequestURI()
	}
	sum := func(s string) string { h := md5.Sum([]byte(s)); return hex.EncodeToString(h[:]) }
	ha1 := sum(user + ":" + f["realm"] + ":" + pass)
	ha2 := sum("GET:" + path)
	out := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s"`, user, f["realm"], f["nonce"], path)
	if strings.Contains(f["qop"], "auth") {
		b := make([]byte, 8)
		rand.Read(b)
		cnonce := hex.EncodeToString(b)
		out += fmt.Sprintf(`, qop=auth, nc=00000001, cnonce="%s", response="%s"`, cnonce, sum(ha1+":"+f["nonce"]+":00000001:"+cnonce+":auth:"+ha2))
	} else {
		out += fmt.Sprintf(`, response="%s"`, sum(ha1+":"+f["nonce"]+":"+ha2))
	}
	if f["opaque"] != "" {
		out += fmt.Sprintf(`, opaque="%s"`, f["opaque"])
	}
	return out
}
