package onvif

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

var jpegBytes = []byte("\xFF\xD8\xFFpicture")

func md5hex(s string) string { h := md5.Sum([]byte(s)); return hex.EncodeToString(h[:]) }

// digestCamera checks a digest login the way a camera does.
func digestCamera(t *testing.T, qop bool) *httptest.Server {
	const nonce, realm = "abc123", "camera"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" {
			challenge := fmt.Sprintf(`Digest realm="%s", nonce="%s"`, realm, nonce)
			if qop {
				challenge += `, qop="auth"`
			}
			w.Header().Set("WWW-Authenticate", challenge)
			w.WriteHeader(401)
			return
		}
		f := map[string]string{}
		for _, m := range regexp.MustCompile(`(\w+)="?([^",]*)"?`).FindAllStringSubmatch(auth, -1) {
			f[m[1]] = m[2]
		}
		ha1, ha2 := md5hex("admin:"+realm+":secret"), md5hex("GET:"+r.URL.RequestURI())
		want := md5hex(ha1 + ":" + nonce + ":" + ha2)
		if qop {
			want = md5hex(ha1 + ":" + nonce + ":" + f["nc"] + ":" + f["cnonce"] + ":auth:" + ha2)
		}
		if f["uri"] != r.URL.RequestURI() || f["response"] != want {
			w.WriteHeader(401)
			return
		}
		w.Write(jpegBytes)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchAnswersDigestLogin(t *testing.T) {
	for _, qop := range []bool{true, false} {
		srv := digestCamera(t, qop)
		c := &Client{User: "admin", Pass: "secret"}
		data, err := c.Fetch(context.Background(), srv.URL+"/snap.jpg?stream=1")
		if err != nil || string(data) != string(jpegBytes) {
			t.Fatalf("qop %v: %v", qop, err)
		}
		c.Pass = "wrong"
		if _, err := c.Fetch(context.Background(), srv.URL+"/snap.jpg"); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("wrong password: %v", err)
		}
	}
}

func TestFetchUsesBasicLoginWhenAsked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "admin" || p != "secret" {
			w.Header().Set("WWW-Authenticate", `Basic realm="camera"`)
			w.WriteHeader(401)
			return
		}
		w.Write(jpegBytes)
	}))
	defer srv.Close()
	if _, err := (&Client{User: "admin", Pass: "secret"}).Fetch(context.Background(), srv.URL); err != nil {
		t.Fatal(err)
	}
}

func TestFetchRefusesWhatIsntAPicture(t *testing.T) {
	bodies := map[string][]byte{
		"not a picture": []byte("<html>login</html>"),
		"too large":     append([]byte("\xFF\xD8"), make([]byte, MaxSnapshot)...),
	}
	for name, body := range bodies {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
		_, err := (&Client{}).Fetch(context.Background(), srv.URL)
		srv.Close()
		if err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestFetchErrorsHideTheAddress(t *testing.T) {
	_, err := (&Client{}).Fetch(context.Background(), "http://admin:topsecret@127.0.0.1:1/snap.jpg")
	if err == nil || strings.Contains(err.Error(), "topsecret") {
		t.Fatalf("%v", err)
	}
}

func TestSnapshotUsesTheLargestProfile(t *testing.T) {
	var asked string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write(jpegBytes)
			return
		}
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		body := string(buf[:n])
		w.Header().Set("Content-Type", "application/soap+xml")
		env := func(s string) string {
			return `<Envelope xmlns="http://www.w3.org/2003/05/soap-envelope"><Body>` + s + `</Body></Envelope>`
		}
		switch {
		case strings.Contains(body, "GetCapabilities"):
			fmt.Fprint(w, env(`<GetCapabilitiesResponse><Capabilities><Media><XAddr>`+srv.URL+`/media</XAddr></Media></Capabilities></GetCapabilitiesResponse>`))
		case strings.Contains(body, "GetProfiles"):
			fmt.Fprint(w, env(`<GetProfilesResponse>
				<Profiles token="small"><VideoEncoderConfiguration><Resolution><Width>640</Width><Height>360</Height></Resolution></VideoEncoderConfiguration></Profiles>
				<Profiles token="big"><VideoEncoderConfiguration><Resolution><Width>1920</Width><Height>1080</Height></Resolution></VideoEncoderConfiguration></Profiles>
			</GetProfilesResponse>`))
		case strings.Contains(body, "GetSnapshotUri"):
			asked = body
			fmt.Fprint(w, env(`<GetSnapshotUriResponse><MediaUri><Uri>`+srv.URL+`/snap.jpg</Uri></MediaUri></GetSnapshotUriResponse>`))
		}
	}))
	defer srv.Close()
	data, err := (&Client{XAddr: srv.URL + "/device"}).Snapshot(context.Background())
	if err != nil || string(data) != string(jpegBytes) {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(asked, "<ProfileToken>big</ProfileToken>") {
		t.Fatal(asked)
	}
}
