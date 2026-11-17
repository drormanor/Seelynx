package rules

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strings"
	"time"
)

var (
	// mailTimeout bounds a whole delivery, so a silent mail server can't hold a send open.
	mailTimeout = 20 * time.Second
	// mailRoots replaces the system's trusted certificates in tests.
	mailRoots *x509.CertPool
)

// graphMaxAttachment keeps a request under Microsoft Graph's size limit once encoded.
const graphMaxAttachment = 2500 << 10

// smtpJob is one delivery. Port 465 starts with TLS; other ports upgrade with STARTTLS.
type smtpJob struct {
	host, port, username, password, from string
	to                                   []string
	implicitTLS                          bool
}

func recipients(list string) []string {
	var to []string
	for _, a := range strings.Split(list, ",") {
		if a = strings.TrimSpace(a); a != "" {
			to = append(to, a)
		}
	}
	return to
}

func sendEmail(ctx context.Context, cfg map[string]string, m Message) error {
	port := cfg["port"]
	if port == "" {
		port = "587"
	}
	return deliverSMTP(ctx, smtpJob{
		host: cfg["host"], port: port, username: cfg["username"], password: cfg["password"],
		from: cfg["from"], to: recipients(cfg["to"]), implicitTLS: port == "465",
	}, m)
}

func sendGmail(ctx context.Context, cfg map[string]string, m Message) error {
	host, port, err := net.SplitHostPort(Endpoints.Gmail)
	if err != nil {
		return err
	}
	address := strings.TrimSpace(cfg["username"])
	return deliverSMTP(ctx, smtpJob{
		host: host, port: port, username: address, from: address, to: recipients(cfg["to"]),
		// Google shows app passwords in groups of four, and people paste them that way.
		password: strings.ReplaceAll(cfg["password"], " ", ""),
	}, m)
}

func deliverSMTP(ctx context.Context, j smtpJob, m Message) error {
	ctx, cancel := context.WithTimeout(ctx, mailTimeout)
	defer cancel()
	config := &tls.Config{ServerName: j.host, RootCAs: mailRoots, MinVersion: tls.VersionTLS12}
	addr := net.JoinHostPort(j.host, j.port)
	var conn net.Conn
	var err error
	if j.implicitTLS {
		conn, err = (&tls.Dialer{Config: config}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	c, err := smtp.NewClient(conn, j.host)
	if err != nil {
		return err
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(config); err != nil {
			return err
		}
	}
	if j.username != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return errors.New("the mail server doesn't offer a login")
		}
		// PlainAuth refuses to send the password over an unencrypted connection.
		if err := c.Auth(smtp.PlainAuth("", j.username, j.password, j.host)); err != nil {
			return err
		}
	}
	if err := c.Mail(j.from); err != nil {
		return err
	}
	for _, a := range j.to {
		if err := c.Rcpt(a); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(mailMessage(j, m)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func mailMessage(j smtpJob, m Message) []byte {
	// One-line values stop a camera name from adding its own mail headers.
	head := "From: " + oneLine(j.from) + "\r\nTo: " + oneLine(strings.Join(j.to, ", ")) +
		"\r\nSubject: " + mime.QEncoding.Encode("utf-8", oneLine(m.Title)) +
		"\r\nDate: " + m.At.Format(time.RFC1123Z) + "\r\nMIME-Version: 1.0\r\n"
	if len(m.Snapshot) == 0 {
		return []byte(head + "Content-Type: text/plain; charset=utf-8\r\n\r\n" + m.Text + "\r\n")
	}
	b := make([]byte, 12)
	rand.Read(b)
	boundary := "seelynx-" + hex.EncodeToString(b)
	var out strings.Builder
	out.WriteString(head + `Content-Type: multipart/mixed; boundary="` + boundary + "\"\r\n\r\n")
	out.WriteString("--" + boundary + "\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + m.Text + "\r\n")
	out.WriteString("--" + boundary + "\r\nContent-Type: image/jpeg; name=\"snapshot.jpg\"\r\n" +
		"Content-Disposition: attachment; filename=\"snapshot.jpg\"\r\nContent-Transfer-Encoding: base64\r\n\r\n")
	encoded := base64.StdEncoding.EncodeToString(m.Snapshot)
	for len(encoded) > 76 { // the line limit for encoded mail
		out.WriteString(encoded[:76] + "\r\n")
		encoded = encoded[76:]
	}
	out.WriteString(encoded + "\r\n--" + boundary + "--\r\n")
	return []byte(out.String())
}

// microsoftToken signs in as the registered app. A token is requested for each message:
// rule cooldowns keep these rare, and nothing has to notice a changed secret.
func microsoftToken(ctx context.Context, cfg map[string]string) (string, error) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {strings.TrimSpace(cfg["client_id"])},
		"client_secret": {strings.TrimSpace(cfg["client_secret"])},
		"scope":         {"https://graph.microsoft.com/.default"},
	}
	target := Endpoints.MicrosoftLogin + "/" + url.PathEscape(strings.TrimSpace(cfg["tenant_id"])) + "/oauth2/v2.0/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return "", errors.New("invalid tenant ID")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := Client.Do(req)
	if err != nil {
		return "", cleanError(err)
	}
	defer res.Body.Close()
	var answer struct {
		Token string `json:"access_token"`
		Error string `json:"error"`
	}
	json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&answer)
	if res.StatusCode >= 300 || answer.Token == "" {
		// Only the short code: the long description can repeat what was sent.
		return "", fmt.Errorf("Microsoft refused the sign-in (%d %s). Check the tenant ID, client ID and client secret", res.StatusCode, shortCode(answer.Error))
	}
	return answer.Token, nil
}

func sendMicrosoft(ctx context.Context, cfg map[string]string, m Message) error {
	token, err := microsoftToken(ctx, cfg)
	if err != nil {
		return err
	}
	var to []map[string]any
	for _, a := range recipients(cfg["to"]) {
		to = append(to, map[string]any{"emailAddress": map[string]string{"address": a}})
	}
	message := map[string]any{
		"subject":      m.Title,
		"body":         map[string]string{"contentType": "Text", "content": m.Text},
		"toRecipients": to,
	}
	if n := len(m.Snapshot); n > 0 && n <= graphMaxAttachment {
		message["attachments"] = []map[string]string{{
			"@odata.type":  "#microsoft.graph.fileAttachment",
			"name":         "snapshot.jpg",
			"contentType":  "image/jpeg",
			"contentBytes": base64.StdEncoding.EncodeToString(m.Snapshot),
		}}
	}
	body, err := json.Marshal(map[string]any{
		"message": message,
		// Alerts would otherwise fill the mailbox's Sent Items.
		"saveToSentItems": false,
	})
	if err != nil {
		return err
	}
	target := Endpoints.Graph + "/v1.0/users/" + url.PathEscape(strings.TrimSpace(cfg["from"])) + "/sendMail"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid mailbox")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := Client.Do(req)
	if err != nil {
		return cleanError(err)
	}
	defer res.Body.Close()
	if res.StatusCode < 300 {
		return nil
	}
	var answer struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&answer)
	hint := ""
	switch res.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		hint = ". Check that the app has the Mail.Send permission for this mailbox. A new permission can take up to 2 hours to apply"
	case http.StatusNotFound:
		hint = ". Check the mailbox address"
	}
	return fmt.Errorf("Microsoft refused the message (%d %s)%s", res.StatusCode, shortCode(answer.Error.Code), hint)
}

func shortCode(s string) string {
	s = oneLine(s)
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}
