package rules

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"seelynx/internal/store"
)

// Field describes one setting of a connection type, so the client can draw its form.
type Field struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Secret      bool   `json:"secret"`
	Required    bool   `json:"required"`
	Placeholder string `json:"placeholder,omitempty"`
}

// Type describes a kind of connection.
type Type struct {
	ID     string  `json:"id"`
	Label  string  `json:"label"`
	Help   string  `json:"help"`
	Fields []Field `json:"fields"`
}

// Types lists every connection Seelynx can make.
var Types = []Type{
	{"webhook", "Webhook", "Sends JSON to any web address. Use this for Zapier, Make, n8n, IFTTT, Home Assistant and your own systems.", []Field{
		{Key: "url", Label: "Web address", Secret: true, Required: true, Placeholder: "https://hooks.example.com/abc"},
		{Key: "header", Label: "Extra header (optional)", Secret: true, Placeholder: "Authorization: Bearer abc123"},
	}},
	{"slack", "Slack", "Uses a Slack incoming webhook.", []Field{
		{Key: "url", Label: "Webhook address", Secret: true, Required: true, Placeholder: "https://hooks.slack.com/services/..."},
	}},
	{"teams", "Microsoft Teams", "Uses a webhook from a Teams workflow.", []Field{
		{Key: "url", Label: "Workflow webhook address", Secret: true, Required: true},
	}},
	{"discord", "Discord", "Uses a channel webhook.", []Field{
		{Key: "url", Label: "Webhook address", Secret: true, Required: true, Placeholder: "https://discord.com/api/webhooks/..."},
	}},
	{"telegram", "Telegram", "Uses a bot from BotFather.", []Field{
		{Key: "token", Label: "Bot token", Secret: true, Required: true},
		{Key: "chat_id", Label: "Chat ID", Required: true},
	}},
	{"gmail", "Gmail or Google Workspace", "Sends through Gmail with an app password. Your normal Google password won't work.", []Field{
		{Key: "username", Label: "Gmail address", Required: true, Placeholder: "alerts@gmail.com"},
		{Key: "password", Label: "App password", Secret: true, Required: true},
		{Key: "to", Label: "To addresses, separated by commas", Required: true},
	}},
	{"microsoft365", "Microsoft 365 email", "Sends through Microsoft Graph, with no mailbox password. An admin sets this up once.", []Field{
		{Key: "tenant_id", Label: "Directory (tenant) ID", Required: true},
		{Key: "client_id", Label: "Application (client) ID", Required: true},
		{Key: "client_secret", Label: "Client secret value", Secret: true, Required: true},
		{Key: "from", Label: "Send from mailbox", Required: true, Placeholder: "alerts@example.com"},
		{Key: "to", Label: "To addresses, separated by commas", Required: true},
	}},
	{"email", "Email (other mail server)", "Sends through any mail server. Port 587 upgrades with STARTTLS when offered, and port 465 uses TLS from the start.", []Field{
		{Key: "host", Label: "Mail server", Required: true, Placeholder: "smtp.example.com"},
		{Key: "port", Label: "Port", Placeholder: "587"},
		{Key: "username", Label: "Username"},
		{Key: "password", Label: "Password", Secret: true},
		{Key: "from", Label: "From address", Required: true},
		{Key: "to", Label: "To addresses, separated by commas", Required: true},
	}},
	{"twilio", "SMS (Twilio)", "Sends text messages through a Twilio account.", []Field{
		{Key: "account_sid", Label: "Account SID", Required: true},
		{Key: "auth_token", Label: "Auth token", Secret: true, Required: true},
		{Key: "from", Label: "From number", Required: true, Placeholder: "+15551234567"},
		{Key: "to", Label: "To number", Required: true, Placeholder: "+15557654321"},
	}},
	{"pagerduty", "PagerDuty", "Opens an incident through the Events API.", []Field{
		{Key: "routing_key", Label: "Integration key", Secret: true, Required: true},
	}},
	{"pushover", "Pushover", "Sends a phone notification.", []Field{
		{Key: "token", Label: "App token", Secret: true, Required: true},
		{Key: "user", Label: "User key", Secret: true, Required: true},
	}},
	{"ntfy", "ntfy", "Publishes to an ntfy topic, on ntfy.sh or your own server.", []Field{
		{Key: "url", Label: "Topic address", Required: true, Placeholder: "https://ntfy.sh/my-topic"},
		{Key: "token", Label: "Access token (optional)", Secret: true},
	}},
	{"mqtt", "MQTT", "Publishes JSON to a broker, for Home Assistant, Node-RED and building systems.", []Field{
		{Key: "host", Label: "Broker address", Required: true, Placeholder: "192.168.1.10:1883"},
		{Key: "topic", Label: "Topic", Required: true, Placeholder: "seelynx/events"},
		{Key: "username", Label: "Username"},
		{Key: "password", Label: "Password", Secret: true},
	}},
}

// FindType returns a connection type by ID.
func FindType(id string) (Type, bool) {
	for _, t := range Types {
		if t.ID == id {
			return t, true
		}
	}
	return Type{}, false
}

// Message is what a connection delivers.
type Message struct {
	Title    string    `json:"title"`
	Text     string    `json:"text"`
	Priority string    `json:"priority"`
	Event    string    `json:"event"`
	Topic    string    `json:"topic,omitempty"`
	Camera   string    `json:"camera,omitempty"`
	CameraID int64     `json:"camera_id,omitempty"`
	At       time.Time `json:"at"`
	Snapshot []byte    `json:"-"` // JPEG picture, when the rule asks for one
}

// Client is used for outgoing calls, and can be replaced in tests.
var Client = &http.Client{Timeout: 10 * time.Second}

// Endpoints are the fixed service addresses, which tests point at a local server.
var Endpoints = struct{ Telegram, Twilio, PagerDuty, Pushover, Gmail, MicrosoftLogin, Graph string }{
	"https://api.telegram.org", "https://api.twilio.com", "https://events.pagerduty.com", "https://api.pushover.net",
	"smtp.gmail.com:587", "https://login.microsoftonline.com", "https://graph.microsoft.com",
}

// cleanError removes the address from an error, because service addresses and bot
// tokens are secrets and errors end up in logs.
func cleanError(err error) error {
	var u *url.Error
	if errors.As(err, &u) {
		return u.Err
	}
	return err
}

func post(ctx context.Context, target, contentType string, body []byte, headers map[string]string) error {
	return request(ctx, http.MethodPost, target, contentType, body, headers)
}

func request(ctx context.Context, method, target, contentType string, body []byte, headers map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid address")
	}
	req.Header.Set("Content-Type", contentType)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := Client.Do(req)
	if err != nil {
		return cleanError(err)
	}
	defer res.Body.Close()
	io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
	if res.StatusCode >= 300 {
		return fmt.Errorf("the service answered %d", res.StatusCode)
	}
	return nil
}

func postJSON(ctx context.Context, target string, v any, headers map[string]string) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return post(ctx, target, "application/json", body, headers)
}

func oneLine(s string) string { return strings.NewReplacer("\r", " ", "\n", " ").Replace(s) }

// Send delivers a message through a connection.
func Send(ctx context.Context, c store.Connection, m Message) error {
	cfg := c.Config
	text := m.Title + ": " + m.Text
	switch c.Type {
	case "webhook":
		headers := map[string]string{}
		if h := cfg["header"]; h != "" {
			k, v, ok := strings.Cut(h, ":")
			if !ok {
				return errors.New("the extra header must look like Name: value")
			}
			headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
		if len(m.Snapshot) > 0 {
			return postJSON(ctx, cfg["url"], struct {
				Message
				Picture string `json:"snapshot"`
			}{m, base64.StdEncoding.EncodeToString(m.Snapshot)}, headers)
		}
		return postJSON(ctx, cfg["url"], m, headers)
	case "slack":
		return postJSON(ctx, cfg["url"], map[string]string{"text": text}, nil)
	case "discord":
		if len(m.Snapshot) > 0 {
			payload, err := json.Marshal(map[string]string{"content": text})
			if err != nil {
				return err
			}
			return postPicture(ctx, cfg["url"], [][2]string{{"payload_json", string(payload)}}, "files[0]", m.Snapshot)
		}
		return postJSON(ctx, cfg["url"], map[string]string{"content": text}, nil)
	case "teams":
		return postJSON(ctx, cfg["url"], map[string]any{
			"type": "message",
			"attachments": []any{map[string]any{
				"contentType": "application/vnd.microsoft.card.adaptive",
				"content": map[string]any{
					"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
					"type":    "AdaptiveCard", "version": "1.4",
					"body": []any{
						map[string]any{"type": "TextBlock", "weight": "Bolder", "text": m.Title, "wrap": true},
						map[string]any{"type": "TextBlock", "text": m.Text, "wrap": true},
					},
				},
			}},
		}, nil)
	case "telegram":
		if len(m.Snapshot) > 0 {
			caption := []rune(text)
			if len(caption) > 1024 { // Telegram's limit for a picture's caption
				caption = caption[:1024]
			}
			return postPicture(ctx, Endpoints.Telegram+"/bot"+cfg["token"]+"/sendPhoto", [][2]string{{"chat_id", cfg["chat_id"]}, {"caption", string(caption)}}, "photo", m.Snapshot)
		}
		return postJSON(ctx, Endpoints.Telegram+"/bot"+cfg["token"]+"/sendMessage", map[string]string{"chat_id": cfg["chat_id"], "text": text}, nil)
	case "pushover":
		form := url.Values{"token": {cfg["token"]}, "user": {cfg["user"]}, "title": {m.Title}, "message": {m.Text}}
		if m.Priority == "high" {
			form.Set("priority", "1")
		}
		if len(m.Snapshot) > 0 {
			var fields [][2]string
			for k, v := range form {
				fields = append(fields, [2]string{k, v[0]})
			}
			return postPicture(ctx, Endpoints.Pushover+"/1/messages.json", fields, "attachment", m.Snapshot)
		}
		return post(ctx, Endpoints.Pushover+"/1/messages.json", "application/x-www-form-urlencoded", []byte(form.Encode()), nil)
	case "ntfy":
		headers := map[string]string{"Title": mime.QEncoding.Encode("utf-8", oneLine(m.Title))}
		if m.Priority == "high" {
			headers["Priority"] = "high"
		}
		if t := cfg["token"]; t != "" {
			headers["Authorization"] = "Bearer " + t
		}
		if len(m.Snapshot) > 0 {
			// The picture is the body, so the text travels in a header.
			headers["Filename"] = "snapshot.jpg"
			headers["Message"] = mime.QEncoding.Encode("utf-8", oneLine(m.Text))
			return request(ctx, http.MethodPut, cfg["url"], "image/jpeg", m.Snapshot, headers)
		}
		return post(ctx, cfg["url"], "text/plain; charset=utf-8", []byte(m.Text), headers)
	case "pagerduty":
		severity := map[string]string{"high": "critical", "medium": "warning", "low": "info"}[m.Priority]
		if severity == "" {
			severity = "warning"
		}
		return postJSON(ctx, Endpoints.PagerDuty+"/v2/enqueue", map[string]any{
			"routing_key": cfg["routing_key"], "event_action": "trigger",
			"payload": map[string]any{"summary": text, "source": "Seelynx", "severity": severity, "timestamp": m.At.Format(time.RFC3339)},
		}, nil)
	case "twilio":
		form := url.Values{"From": {cfg["from"]}, "To": {cfg["to"]}, "Body": {text}}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, Endpoints.Twilio+"/2010-04-01/Accounts/"+url.PathEscape(cfg["account_sid"])+"/Messages.json", strings.NewReader(form.Encode()))
		if err != nil {
			return errors.New("invalid account")
		}
		req.SetBasicAuth(cfg["account_sid"], cfg["auth_token"])
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res, err := Client.Do(req)
		if err != nil {
			return cleanError(err)
		}
		defer res.Body.Close()
		if res.StatusCode >= 300 {
			return fmt.Errorf("the service answered %d", res.StatusCode)
		}
		return nil
	case "email":
		return sendEmail(ctx, cfg, m)
	case "gmail":
		return sendGmail(ctx, cfg, m)
	case "microsoft365":
		return sendMicrosoft(ctx, cfg, m)
	case "mqtt":
		body, err := json.Marshal(m)
		if err != nil {
			return err
		}
		return publishMQTT(ctx, cfg, body)
	}
	return fmt.Errorf("unknown connection type %q", c.Type)
}

func mqttString(s string) []byte {
	b := []byte{byte(len(s) >> 8), byte(len(s))}
	return append(b, s...)
}

func mqttPacket(kind byte, body []byte) []byte {
	out := []byte{kind}
	n := len(body)
	for {
		d := byte(n % 128)
		n /= 128
		if n > 0 {
			d |= 0x80
		}
		out = append(out, d)
		if n == 0 {
			break
		}
	}
	return append(out, body...)
}

// publishMQTT sends one QoS 0 message over a short-lived MQTT 3.1.1 connection.
func publishMQTT(ctx context.Context, cfg map[string]string, payload []byte) error {
	host := cfg["host"]
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "1883")
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(8 * time.Second))

	flags := byte(0x02) // clean session
	body := append(mqttString("MQTT"), 4, 0, 0, 30)
	body = append(body, mqttString("seelynx-"+strconv.FormatInt(time.Now().UnixNano(), 36))...)
	if cfg["username"] != "" {
		flags |= 0x80
		body = append(body, mqttString(cfg["username"])...)
		if cfg["password"] != "" {
			flags |= 0x40
			body = append(body, mqttString(cfg["password"])...)
		}
	}
	body[7] = flags
	if _, err := conn.Write(mqttPacket(0x10, body)); err != nil {
		return err
	}
	ack := make([]byte, 4)
	if _, err := io.ReadFull(conn, ack); err != nil {
		return errors.New("the broker didn't answer")
	}
	if ack[0] != 0x20 || ack[3] != 0 {
		return fmt.Errorf("the broker refused the connection (code %d)", ack[3])
	}
	if _, err := conn.Write(mqttPacket(0x30, append(mqttString(cfg["topic"]), payload...))); err != nil {
		return err
	}
	_, err = conn.Write([]byte{0xE0, 0})
	return err
}
