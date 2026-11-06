package notify

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Phone ping (ADR-0021): one POST to daemon.notify_url for each event that
// raises an OS notification. The body is the name and what happened, built
// from the event's kind and name. It never reads Summary, so no prompt,
// answer or command leaves the machine. No error here names the URL, because
// the URL can hold the secret of a topic.

// pingTimeout bounds one POST, from the dial to the end of the answer.
const pingTimeout = 5 * time.Second

// pingSlots caps the POSTs that run at once. A hung server fills them, and
// further events lose their ping, not their time.
const pingSlots = 16

// PingTestText is the body of `smif notify test`.
const PingTestText = "smif test ping"

// PingText is the body of the ping for an event.
func PingText(e Event) string {
	return Clip(strings.TrimPrefix(title(e), "Smif: "), 280)
}

// Pinger posts pings. It holds no URL: the caller gives the current one for
// each ping, so a config reload needs no new Pinger.
type Pinger struct {
	client *http.Client
	slots  chan struct{}
	failed func(kind, why string)
}

// NewPinger builds a Pinger. failed hears each lost ping of Send, from
// another goroutine, and never gets the URL.
func NewPinger(failed func(kind, why string)) *Pinger {
	return &Pinger{
		client: &http.Client{
			Timeout: pingTimeout,
			// A redirect could lead from https to plain http.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		slots:  make(chan struct{}, pingSlots),
		failed: failed,
	}
}

// Send posts the ping for e in the background and returns at once. An empty
// URL sends nothing. There is no retry.
func (p *Pinger) Send(url string, e Event) {
	if url == "" {
		return
	}
	select {
	case p.slots <- struct{}{}:
	default:
		p.failed(e.Kind, "too many pings wait for an answer")
		return
	}
	go func() {
		defer func() { <-p.slots }()
		if err := p.Post(url, PingText(e)); err != nil {
			p.failed(e.Kind, err.Error())
		}
	}()
}

// Post sends one ping and waits for the answer. Any 2xx is success.
func (p *Pinger) Post(url, text string) error {
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(text))
	if err != nil {
		return errors.New("the URL does not parse")
	}
	req.Header.Set("Title", "smif")
	req.Header.Set("Tags", "bell")
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	resp, err := p.client.Do(req)
	if err != nil {
		return pingError(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("the server answered %d", resp.StatusCode)
	}
	return nil
}

// pingError drops the text of err: net/http quotes the URL, and a dial error
// names the host.
func pingError(err error) error {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return fmt.Errorf("no answer in %d s", int(pingTimeout.Seconds()))
	}
	return errors.New("the connection failed")
}
