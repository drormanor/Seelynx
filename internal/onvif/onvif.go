// Package onvif finds cameras with WS-Discovery and reads their stream URLs.
package onvif

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type Device struct {
	XAddr string `json:"xaddr"`
	Host  string `json:"host"`
	Name  string `json:"name"`
}

type Profile struct {
	Token  string `json:"token"`
	Name   string `json:"name"`
	Codec  string `json:"codec"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	URI    string `json:"uri"`
}

var ErrUnauthorized = errors.New("camera rejected the username or password")

func uuid() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

const probeTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<e:Envelope xmlns:e="http://www.w3.org/2003/05/soap-envelope" xmlns:w="http://schemas.xmlsoap.org/ws/2004/08/addressing" xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery" xmlns:dn="http://www.onvif.org/ver10/network/wsdl">
<e:Header><w:MessageID>uuid:%s</w:MessageID><w:To e:mustUnderstand="true">urn:schemas-xmlsoap-org:ws:2005:04:discovery</w:To><w:Action e:mustUnderstand="true">http://schemas.xmlsoap.org/ws/2005/04/discovery/Probe</w:Action></e:Header>
<e:Body><d:Probe><d:Types>dn:NetworkVideoTransmitter</d:Types></d:Probe></e:Body>
</e:Envelope>`

type probeMatches struct {
	Matches []struct {
		Scopes string `xml:"Scopes"`
		XAddrs string `xml:"XAddrs"`
	} `xml:"Body>ProbeMatches>ProbeMatch"`
}

// ParseProbeMatch extracts devices from one WS-Discovery response.
func ParseProbeMatch(data []byte) []Device {
	var pm probeMatches
	if xml.Unmarshal(data, &pm) != nil {
		return nil
	}
	var out []Device
	for _, m := range pm.Matches {
		fields := strings.Fields(m.XAddrs)
		if len(fields) == 0 {
			continue
		}
		u, err := url.Parse(fields[0])
		if err != nil || u.Host == "" {
			continue
		}
		d := Device{XAddr: fields[0], Host: u.Hostname()}
		for _, s := range strings.Fields(m.Scopes) {
			if name, ok := strings.CutPrefix(s, "onvif://www.onvif.org/name/"); ok {
				if v, err := url.PathUnescape(name); err == nil {
					d.Name = v
				}
			}
		}
		out = append(out, d)
	}
	return out
}

// Discover multicasts a probe from every IPv4 interface and collects replies.
func Discover(ctx context.Context, wait time.Duration) ([]Device, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	dst := &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 3702}
	found := make(chan []Device, 64)
	deadline := time.Now().Add(wait)
	listeners := 0
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.To4() == nil || ipn.IP.IsLoopback() {
			continue
		}
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ipn.IP})
		if err != nil {
			continue
		}
		listeners++
		go func() {
			defer conn.Close()
			conn.SetDeadline(deadline)
			conn.WriteToUDP([]byte(fmt.Sprintf(probeTemplate, uuid())), dst)
			buf := make([]byte, 65536)
			for {
				n, _, err := conn.ReadFromUDP(buf)
				if err != nil {
					found <- nil // marks this listener finished
					return
				}
				if ds := ParseProbeMatch(buf[:n]); len(ds) > 0 {
					found <- ds
				}
			}
		}()
	}
	seen := map[string]Device{}
	for done := 0; done < listeners; {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case ds := <-found:
			if ds == nil {
				done++
			}
			for _, d := range ds {
				seen[d.XAddr] = d
			}
		}
	}
	out := make([]Device, 0, len(seen))
	for _, d := range seen {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out, nil
}

// securityHeader builds a WS-Security UsernameToken with a password digest.
func securityHeader(user, pass string, now time.Time) string {
	if user == "" {
		return ""
	}
	nonce := make([]byte, 16)
	rand.Read(nonce)
	created := now.UTC().Format("2006-01-02T15:04:05Z")
	h := sha1.New()
	h.Write(nonce)
	h.Write([]byte(created))
	h.Write([]byte(pass))
	var esc bytes.Buffer
	xml.EscapeText(&esc, []byte(user))
	return fmt.Sprintf(`<Security s:mustUnderstand="1" xmlns="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd"><UsernameToken><Username>%s</Username><Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest">%s</Password><Nonce EncodingType="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-soap-message-security-1.0#Base64Binary">%s</Nonce><Created xmlns="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd">%s</Created></UsernameToken></Security>`,
		esc.String(), base64.StdEncoding.EncodeToString(h.Sum(nil)), base64.StdEncoding.EncodeToString(nonce), created)
}

type Client struct {
	XAddr, User, Pass string
	Offset            time.Duration // camera clock minus ours, see SyncClock
	HTTP              *http.Client
}

func (c *Client) call(ctx context.Context, endpoint, body string, out any) error {
	env := `<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Header>` +
		securityHeader(c.User, c.Pass, time.Now().Add(c.Offset)) + `</s:Header><s:Body>` + body + `</s:Body></s:Envelope>`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(env))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	res, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return err
	}
	if res.StatusCode == http.StatusUnauthorized || bytes.Contains(data, []byte("NotAuthorized")) {
		return ErrUnauthorized
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("camera returned HTTP %d", res.StatusCode)
	}
	return xml.Unmarshal(data, out)
}

// Profiles reads the camera's media profiles and each one's RTSP URL.
func (c *Client) Profiles(ctx context.Context) ([]Profile, error) {
	var caps struct {
		XAddr string `xml:"Body>GetCapabilitiesResponse>Capabilities>Media>XAddr"`
	}
	err := c.call(ctx, c.XAddr, `<GetCapabilities xmlns="http://www.onvif.org/ver10/device/wsdl"><Category>Media</Category></GetCapabilities>`, &caps)
	if err != nil {
		return nil, err
	}
	if caps.XAddr == "" {
		return nil, errors.New("camera reported no media service")
	}
	var profiles struct {
		Items []struct {
			Token string `xml:"token,attr"`
			Name  string `xml:"Name"`
			Video struct {
				Encoding string `xml:"Encoding"`
				Width    int    `xml:"Resolution>Width"`
				Height   int    `xml:"Resolution>Height"`
			} `xml:"VideoEncoderConfiguration"`
		} `xml:"Body>GetProfilesResponse>Profiles"`
	}
	if err := c.call(ctx, caps.XAddr, `<GetProfiles xmlns="http://www.onvif.org/ver10/media/wsdl"/>`, &profiles); err != nil {
		return nil, err
	}
	out := make([]Profile, 0, len(profiles.Items))
	for _, p := range profiles.Items {
		var esc bytes.Buffer
		xml.EscapeText(&esc, []byte(p.Token))
		var uri struct {
			URI string `xml:"Body>GetStreamUriResponse>MediaUri>Uri"`
		}
		req := `<GetStreamUri xmlns="http://www.onvif.org/ver10/media/wsdl"><StreamSetup><Stream xmlns="http://www.onvif.org/ver10/schema">RTP-Unicast</Stream><Transport xmlns="http://www.onvif.org/ver10/schema"><Protocol>RTSP</Protocol></Transport></StreamSetup><ProfileToken>` + esc.String() + `</ProfileToken></GetStreamUri>`
		if err := c.call(ctx, caps.XAddr, req, &uri); err != nil {
			return nil, err
		}
		out = append(out, Profile{Token: p.Token, Name: p.Name, Codec: p.Video.Encoding, Width: p.Video.Width, Height: p.Video.Height, URI: uri.URI})
	}
	// Largest first: the first profile is the natural main stream.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Width*out[i].Height > out[j].Width*out[j].Height })
	return out, nil
}
