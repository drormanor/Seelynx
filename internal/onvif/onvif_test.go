package onvif

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseProbeMatch(t *testing.T) {
	msg := `<e:Envelope xmlns:e="http://www.w3.org/2003/05/soap-envelope" xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery"><e:Body><d:ProbeMatches><d:ProbeMatch>
	<d:Scopes>onvif://www.onvif.org/type/video onvif://www.onvif.org/name/Lobby%20Cam</d:Scopes>
	<d:XAddrs>http://192.0.2.5/onvif/device_service http://[fe80::1]/onvif/device_service</d:XAddrs>
	</d:ProbeMatch></d:ProbeMatches></e:Body></e:Envelope>`
	got := ParseProbeMatch([]byte(msg))
	if len(got) != 1 || got[0].Host != "192.0.2.5" || got[0].Name != "Lobby Cam" || got[0].XAddr != "http://192.0.2.5/onvif/device_service" {
		t.Fatalf("unexpected: %+v", got)
	}
	if ParseProbeMatch([]byte("not xml")) != nil {
		t.Fatal("garbage should yield nothing")
	}
}

func TestSecurityHeaderDigest(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var h struct {
		Password string `xml:"UsernameToken>Password"`
		Nonce    string `xml:"UsernameToken>Nonce"`
		Created  string `xml:"UsernameToken>Created"`
		Username string `xml:"UsernameToken>Username"`
	}
	raw := strings.Replace(securityHeader("a&b", "secret", now), `s:mustUnderstand="1" `, "", 1)
	if err := xml.Unmarshal([]byte(raw), &h); err != nil {
		t.Fatal(err)
	}
	if h.Username != "a&b" || h.Created != "2026-01-02T03:04:05Z" {
		t.Fatalf("unexpected: %+v", h)
	}
	nonce, _ := base64.StdEncoding.DecodeString(h.Nonce)
	sum := sha1.Sum(append(append(nonce, h.Created...), "secret"...))
	if base64.StdEncoding.EncodeToString(sum[:]) != h.Password {
		t.Fatal("digest does not follow the WS-Security formula")
	}
}

func fakeCamera(t *testing.T, requireAuth bool) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body := string(b)
		if requireAuth && !strings.Contains(body, "UsernameToken") {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `<Envelope><Body><Fault><Subcode><Value>ter:NotAuthorized</Value></Subcode></Fault></Body></Envelope>`)
			return
		}
		env := func(s string) {
			io.WriteString(w, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:t="urn:t"><s:Body>`+s+`</s:Body></s:Envelope>`)
		}
		switch {
		case strings.Contains(body, "GetCapabilities"):
			env(`<t:GetCapabilitiesResponse><t:Capabilities><t:Media><t:XAddr>` + srv.URL + `/media</t:XAddr></t:Media></t:Capabilities></t:GetCapabilitiesResponse>`)
		case strings.Contains(body, "GetProfiles"):
			env(`<t:GetProfilesResponse>
			<t:Profiles token="sub"><t:Name>Low</t:Name><t:VideoEncoderConfiguration><t:Encoding>H264</t:Encoding><t:Resolution><t:Width>640</t:Width><t:Height>360</t:Height></t:Resolution></t:VideoEncoderConfiguration></t:Profiles>
			<t:Profiles token="main"><t:Name>High</t:Name><t:VideoEncoderConfiguration><t:Encoding>H264</t:Encoding><t:Resolution><t:Width>1920</t:Width><t:Height>1080</t:Height></t:Resolution></t:VideoEncoderConfiguration></t:Profiles>
			</t:GetProfilesResponse>`)
		case strings.Contains(body, "<ProfileToken>main<"):
			env(`<t:GetStreamUriResponse><t:MediaUri><t:Uri>rtsp://cam/main</t:Uri></t:MediaUri></t:GetStreamUriResponse>`)
		case strings.Contains(body, "<ProfileToken>sub<"):
			env(`<t:GetStreamUriResponse><t:MediaUri><t:Uri>rtsp://cam/sub</t:Uri></t:MediaUri></t:GetStreamUriResponse>`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProfilesLargestFirst(t *testing.T) {
	srv := fakeCamera(t, true)
	c := Client{XAddr: srv.URL + "/device", User: "admin", Pass: "pw"}
	ps, err := c.Profiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 || ps[0].URI != "rtsp://cam/main" || ps[0].Width != 1920 || ps[1].URI != "rtsp://cam/sub" || ps[1].Codec != "H264" {
		t.Fatalf("unexpected: %+v", ps)
	}
}

func TestProfilesRejectedCredentials(t *testing.T) {
	srv := fakeCamera(t, true)
	c := Client{XAddr: srv.URL + "/device"}
	if _, err := c.Profiles(context.Background()); err != ErrUnauthorized {
		t.Fatalf("got %v", err)
	}
}

func TestParseEvents(t *testing.T) {
	msg := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:t="urn:e" xmlns:w="urn:w" xmlns:o="urn:o"><s:Body><t:PullMessagesResponse>
	<w:NotificationMessage><w:Topic Dialect="d">tns1:VideoSource/MotionAlarm</w:Topic><w:Message><o:Message UtcTime="2026-11-04T20:00:00Z">
	<o:Source><o:SimpleItem Name="Source" Value="vs1"/></o:Source><o:Data><o:SimpleItem Name="State" Value="true"/></o:Data></o:Message></w:Message></w:NotificationMessage>
	</t:PullMessagesResponse></s:Body></s:Envelope>`
	got, err := ParseEvents([]byte(msg))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Topic != "tns1:VideoSource/MotionAlarm" || got[0].Items["State"] != "true" || got[0].Items["Source"] != "vs1" || got[0].Time != "2026-11-04T20:00:00Z" {
		t.Fatalf("%+v", got)
	}
}
