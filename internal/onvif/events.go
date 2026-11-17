package onvif

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	eventsNS = "http://www.onvif.org/ver10/events/wsdl"
	wsaNS    = "http://www.w3.org/2005/08/addressing"
)

// SyncClock measures the camera's clock against ours. WS-Security digests include a
// timestamp, so a camera with a wrong clock can refuse an otherwise correct login.
func (c *Client) SyncClock(ctx context.Context) error {
	var out struct {
		T struct {
			Hour   int `xml:"Time>Hour"`
			Minute int `xml:"Time>Minute"`
			Second int `xml:"Time>Second"`
			Year   int `xml:"Date>Year"`
			Month  int `xml:"Date>Month"`
			Day    int `xml:"Date>Day"`
		} `xml:"Body>GetSystemDateAndTimeResponse>SystemDateAndTime>UTCDateTime"`
	}
	saved := c.User
	c.User = "" // this call is unauthenticated by design
	err := c.call(ctx, c.XAddr, `<GetSystemDateAndTime xmlns="http://www.onvif.org/ver10/device/wsdl"/>`, &out)
	c.User = saved
	if err != nil {
		return err
	}
	if out.T.Year == 0 {
		return errors.New("camera reported no time")
	}
	cam := time.Date(out.T.Year, time.Month(out.T.Month), out.T.Day, out.T.Hour, out.T.Minute, out.T.Second, 0, time.UTC)
	c.Offset = time.Until(cam)
	return nil
}

// raw sends a SOAP request with WS-Addressing headers and returns the response body.
func (c *Client) raw(ctx context.Context, endpoint, action, body string) ([]byte, error) {
	env := `<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:a="` + wsaNS + `"><s:Header>` +
		`<a:Action s:mustUnderstand="1">` + action + `</a:Action><a:To s:mustUnderstand="1">` + endpoint + `</a:To>` +
		securityHeader(c.User, c.Pass, time.Now().Add(c.Offset)) + `</s:Header><s:Body>` + body + `</s:Body></s:Envelope>`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(env))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode == http.StatusUnauthorized || bytes.Contains(data, []byte("NotAuthorized")) {
		return nil, ErrUnauthorized
	}
	if res.StatusCode != http.StatusOK {
		return data, fmt.Errorf("camera returned HTTP %d", res.StatusCode)
	}
	return data, nil
}

// EventsAddress returns the camera's event service URL.
func (c *Client) EventsAddress(ctx context.Context) (string, error) {
	var caps struct {
		XAddr string `xml:"Body>GetCapabilitiesResponse>Capabilities>Events>XAddr"`
	}
	err := c.call(ctx, c.XAddr, `<GetCapabilities xmlns="http://www.onvif.org/ver10/device/wsdl"><Category>Events</Category></GetCapabilities>`, &caps)
	if err != nil {
		return "", err
	}
	if caps.XAddr == "" {
		return "", errors.New("camera reported no event service")
	}
	return caps.XAddr, nil
}

// EventProperties returns the raw list of topics the camera can report.
func (c *Client) EventProperties(ctx context.Context, addr string) ([]byte, error) {
	return c.raw(ctx, addr, eventsNS+"/EventPortType/GetEventPropertiesRequest", `<GetEventProperties xmlns="`+eventsNS+`"/>`)
}

// Subscribe creates a pull point and returns its address.
func (c *Client) Subscribe(ctx context.Context, addr string, ttl time.Duration) (string, error) {
	body := fmt.Sprintf(`<CreatePullPointSubscription xmlns="%s"><InitialTerminationTime>PT%dS</InitialTerminationTime></CreatePullPointSubscription>`, eventsNS, int(ttl.Seconds()))
	data, err := c.raw(ctx, addr, eventsNS+"/EventPortType/CreatePullPointSubscriptionRequest", body)
	if err != nil {
		return "", err
	}
	var out struct {
		Address string `xml:"Body>CreatePullPointSubscriptionResponse>SubscriptionReference>Address"`
	}
	if err := xml.Unmarshal(data, &out); err != nil {
		return "", err
	}
	if out.Address == "" {
		return "", errors.New("camera returned no subscription address")
	}
	return strings.TrimSpace(out.Address), nil
}

// Event is one notification.
type Event struct {
	// Operation is "Initialized" for the current state sent on subscribing, which isn't
	// something that just happened, and "Changed" otherwise.
	Operation string
	Topic     string
	Time      string
	Items     map[string]string // Source and Data simple items
}

type simpleItem struct {
	Name  string `xml:"Name,attr"`
	Value string `xml:"Value,attr"`
}

// ParseEvents extracts notifications from a PullMessages response.
func ParseEvents(data []byte) ([]Event, error) {
	var out struct {
		Messages []struct {
			Topic   string `xml:"Topic"`
			Message struct {
				Inner struct {
					Time   string       `xml:"UtcTime,attr"`
					Op     string       `xml:"PropertyOperation,attr"`
					Source []simpleItem `xml:"Source>SimpleItem"`
					Data   []simpleItem `xml:"Data>SimpleItem"`
				} `xml:"Message"`
			} `xml:"Message"`
		} `xml:"Body>PullMessagesResponse>NotificationMessage"`
	}
	if err := xml.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	events := make([]Event, 0, len(out.Messages))
	for _, m := range out.Messages {
		e := Event{Topic: strings.TrimSpace(m.Topic), Time: m.Message.Inner.Time, Operation: m.Message.Inner.Op, Items: map[string]string{}}
		for _, it := range append(m.Message.Inner.Source, m.Message.Inner.Data...) {
			e.Items[it.Name] = it.Value
		}
		events = append(events, e)
	}
	return events, nil
}

// Pull waits up to timeout for notifications.
func (c *Client) Pull(ctx context.Context, sub string, timeout time.Duration) ([]Event, error) {
	body := fmt.Sprintf(`<PullMessages xmlns="%s"><Timeout>PT%dS</Timeout><MessageLimit>100</MessageLimit></PullMessages>`, eventsNS, int(timeout.Seconds()))
	data, err := c.raw(ctx, sub, eventsNS+"/PullPointSubscription/PullMessagesRequest", body)
	if err != nil {
		return nil, err
	}
	return ParseEvents(data)
}

const wsnNS = "http://docs.oasis-open.org/wsn/b-2"

// Renew extends a subscription.
func (c *Client) Renew(ctx context.Context, sub string, ttl time.Duration) error {
	body := fmt.Sprintf(`<Renew xmlns="%s"><TerminationTime>PT%dS</TerminationTime></Renew>`, wsnNS, int(ttl.Seconds()))
	_, err := c.raw(ctx, sub, "http://docs.oasis-open.org/wsn/bw-2/SubscriptionManager/RenewRequest", body)
	return err
}

// Unsubscribe ends a subscription.
func (c *Client) Unsubscribe(ctx context.Context, sub string) error {
	_, err := c.raw(ctx, sub, "http://docs.oasis-open.org/wsn/bw-2/SubscriptionManager/UnsubscribeRequest", `<Unsubscribe xmlns="`+wsnNS+`"/>`)
	return err
}
