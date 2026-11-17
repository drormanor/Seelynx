package rules

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"seelynx/internal/store"
)

// fakeMail is a small SMTP server. It only offers a login once the connection is encrypted,
// as Gmail does.
type fakeMail struct {
	mu                sync.Mutex
	login, data, rcpt string
	loginOverTLS      bool
}

func (f *fakeMail) snapshot() fakeMail {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fakeMail{login: f.login, data: f.data, rcpt: f.rcpt, loginOverTLS: f.loginOverTLS}
}

func (f *fakeMail) serve(conn net.Conn, config *tls.Config, startTLS bool) {
	defer func() { conn.Close() }()
	_, encrypted := conn.(*tls.Conn)
	r := bufio.NewReader(conn)
	say := func(s string) { io.WriteString(conn, s+"\r\n") }
	say("220 fake")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimSpace(line)
		switch strings.ToUpper(strings.SplitN(line, " ", 2)[0]) {
		case "EHLO":
			switch {
			case encrypted:
				say("250-fake\r\n250 AUTH PLAIN")
			case startTLS:
				say("250-fake\r\n250 STARTTLS")
			default:
				say("250 fake")
			}
		case "STARTTLS":
			say("220 go")
			conn = tls.Server(conn, config)
			r = bufio.NewReader(conn)
			encrypted = true
		case "AUTH":
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "AUTH PLAIN "))
			f.mu.Lock()
			f.login, f.loginOverTLS = string(raw), encrypted
			f.mu.Unlock()
			say("235 ok")
		case "RCPT":
			f.mu.Lock()
			f.rcpt += line + "\n"
			f.mu.Unlock()
			say("250 ok")
		case "DATA":
			say("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.mu.Lock()
			f.data = b.String()
			f.mu.Unlock()
			say("250 ok")
		case "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

// mailServer starts a fake server and trusts its certificate for the test.
func mailServer(t *testing.T, implicitTLS, startTLS bool) (*fakeMail, string, string) {
	t.Helper()
	cert := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(cert.Close)
	roots := x509.NewCertPool()
	roots.AddCert(cert.Certificate())
	saved := mailRoots
	mailRoots = roots
	t.Cleanup(func() { mailRoots = saved })
	config := &tls.Config{Certificates: cert.TLS.Certificates}

	var l net.Listener
	var err error
	if implicitTLS {
		l, err = tls.Listen("tcp", "127.0.0.1:0", config)
	} else {
		l, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	f := &fakeMail{}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go f.serve(c, config, startTLS)
		}
	}()
	host, port, _ := net.SplitHostPort(l.Addr().String())
	return f, host, port
}

func TestGmailUpgradesToTLSBeforeLogin(t *testing.T) {
	f, host, port := mailServer(t, false, true)
	saved := Endpoints
	defer func() { Endpoints = saved }()
	Endpoints.Gmail = net.JoinHostPort(host, port)

	cfg := map[string]string{"username": "me@gmail.com", "password": "abcd efgh ijkl mnop", "to": "a@example.com, b@example.com"}
	if err := Send(context.Background(), store.Connection{Type: "gmail", Config: cfg}, sample); err != nil {
		t.Fatal(err)
	}
	got := f.snapshot()
	if !got.loginOverTLS {
		t.Error("logged in without encryption")
	}
	if got.login != "\x00me@gmail.com\x00abcdefghijklmnop" {
		t.Errorf("login %q", got.login)
	}
	if !strings.Contains(got.data, "From: me@gmail.com") || !strings.Contains(got.data, "Motion at the door") {
		t.Errorf("message %q", got.data)
	}
	if strings.Count(got.rcpt, "RCPT") != 2 {
		t.Errorf("recipients %q", got.rcpt)
	}
}

func TestPort465StartsWithTLS(t *testing.T) {
	f, host, port := mailServer(t, true, false)
	job := smtpJob{host: host, port: port, username: "u", password: "p", from: "a@example.com", to: []string{"b@example.com"}, implicitTLS: true}
	if err := deliverSMTP(context.Background(), job, sample); err != nil {
		t.Fatal(err)
	}
	if got := f.snapshot(); !got.loginOverTLS || !strings.Contains(got.data, "Motion at the door") {
		t.Errorf("tls %v, message %q", got.loginOverTLS, got.data)
	}
}

func TestLoginNeedsAnEncryptedServer(t *testing.T) {
	// The server offers no encryption and no login, so nothing can be sent.
	f, host, port := mailServer(t, false, false)
	job := smtpJob{host: host, port: port, username: "u", password: "p", from: "a@example.com", to: []string{"b@example.com"}}
	if err := deliverSMTP(context.Background(), job, sample); err == nil {
		t.Fatal("expected an error")
	}
	if got := f.snapshot(); got.login != "" {
		t.Errorf("login %q", got.login)
	}
}

func TestSubjectCarriesOtherAlphabets(t *testing.T) {
	f, host, port := mailServer(t, false, false)
	m := sample
	m.Title = "Seelynx: דלת"
	job := smtpJob{host: host, port: port, from: "a@example.com", to: []string{"b@example.com"}}
	if err := deliverSMTP(context.Background(), job, m); err != nil {
		t.Fatal(err)
	}
	if got := f.snapshot(); !strings.Contains(got.data, "Subject: =?utf-8?q?") {
		t.Errorf("message %q", got.data)
	}
}

func TestSilentMailServerTimesOut(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		c, err := l.Accept()
		if err == nil {
			defer c.Close()
			io.Copy(io.Discard, c)
		}
	}()
	saved := mailTimeout
	mailTimeout = 200 * time.Millisecond
	defer func() { mailTimeout = saved }()
	host, port, _ := net.SplitHostPort(l.Addr().String())
	start := time.Now()
	err = deliverSMTP(context.Background(), smtpJob{host: host, port: port, from: "a@example.com", to: []string{"b@example.com"}}, sample)
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("%v after %v", err, time.Since(start))
	}
}

// microsoftServer stands in for both Microsoft addresses. It returns what the sign-in
// form and the send call carried.
func microsoftServer(t *testing.T, sendStatus int) (*url.Values, *string) {
	t.Helper()
	token := &url.Values{}
	sent := new(string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		switch {
		case r.URL.Path == "/my-tenant/oauth2/v2.0/token":
			*token, _ = url.ParseQuery(string(b))
			if token.Get("client_secret") != "good-secret" {
				w.WriteHeader(401)
				io.WriteString(w, `{"error":"invalid_client","error_description":"bad value good-guess"}`)
				return
			}
			io.WriteString(w, `{"access_token":"tok-1"}`)
		case r.URL.Path == "/v1.0/users/alerts@example.com/sendMail" && r.Header.Get("Authorization") == "Bearer tok-1":
			*sent = string(b)
			w.WriteHeader(sendStatus)
			if sendStatus >= 300 {
				io.WriteString(w, `{"error":{"code":"ErrorAccessDenied","message":"no"}}`)
			}
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	saved := Endpoints
	t.Cleanup(func() { Endpoints = saved })
	Endpoints.MicrosoftLogin, Endpoints.Graph = srv.URL, srv.URL
	return token, sent
}

func microsoftConfig(secret string) store.Connection {
	return store.Connection{Type: "microsoft365", Config: map[string]string{
		"tenant_id": " my-tenant ", "client_id": "app-1", "client_secret": secret,
		"from": "alerts@example.com", "to": "a@example.com, b@example.com",
	}}
}

func TestMicrosoftSendsThroughGraph(t *testing.T) {
	token, sent := microsoftServer(t, 202)
	if err := Send(context.Background(), microsoftConfig("good-secret"), sample); err != nil {
		t.Fatal(err)
	}
	if token.Get("grant_type") != "client_credentials" || token.Get("client_id") != "app-1" || token.Get("scope") != "https://graph.microsoft.com/.default" {
		t.Errorf("%v", *token)
	}
	var got struct {
		Message struct {
			Subject      string
			Body         struct{ ContentType, Content string }
			ToRecipients []struct{ EmailAddress struct{ Address string } }
		}
		SaveToSentItems *bool
	}
	if err := json.Unmarshal([]byte(*sent), &got); err != nil {
		t.Fatal(err)
	}
	if got.Message.Subject != sample.Title || got.Message.Body.ContentType != "Text" || got.Message.Body.Content != sample.Text {
		t.Errorf("%+v", got)
	}
	if len(got.Message.ToRecipients) != 2 || got.Message.ToRecipients[1].EmailAddress.Address != "b@example.com" {
		t.Errorf("%+v", got.Message.ToRecipients)
	}
	if got.SaveToSentItems == nil || *got.SaveToSentItems {
		t.Error("alerts would fill Sent Items")
	}
}

func TestMicrosoftErrorsExplainAndHideSecrets(t *testing.T) {
	microsoftServer(t, 403)
	err := Send(context.Background(), microsoftConfig("good-guess"), sample)
	if err == nil || !strings.Contains(err.Error(), "invalid_client") || strings.Contains(err.Error(), "good-guess") {
		t.Fatalf("%v", err)
	}
	err = Send(context.Background(), microsoftConfig("good-secret"), sample)
	if err == nil || !strings.Contains(err.Error(), "ErrorAccessDenied") || !strings.Contains(err.Error(), "Mail.Send") {
		t.Fatalf("%v", err)
	}
}
