package sandbox

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
)

// fakeEngine answers one request on an in-memory connection with the raw
// HTTP response given, the way a process squatting on the Docker socket or
// pipe could. No listener, no network.
func fakeEngine(t *testing.T, response string) func(context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			if _, err := http.ReadRequest(bufio.NewReader(server)); err != nil {
				return
			}
			_, _ = server.Write([]byte(response))
		}()
		go func() { <-ctx.Done(); client.Close() }()
		return client, nil
	}
}

// TestDockerPing_ErrorKeepsOnlyStatusCode guards the path the M2-3 review
// confirmed: an engine answering "HTTP/1.1 500 <reason>" put the reason
// phrase, chosen by whoever answers on the endpoint, into the error and from
// there into the doctor Detail, raw C1 controls and all and without a length
// bound. Only the numeric code may survive, at the probe and in the check.
// (M2-17: the probe is now the gate's fetch, GET /version then GET /info.)
func TestDockerPing_ErrorKeepsOnlyStatusCode(t *testing.T) {
	reason := "x" + string(rune(0x9d)) + "0;pwn" + string(rune(0x9c)) + string(rune(0x9b)) + "2J" + strings.Repeat("A", 64<<10)
	dial := fakeEngine(t, "HTTP/1.1 500 "+reason+"\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")

	_, err := fetchEngineVia(context.Background(), dial)
	var ae *engineAnswerError
	if !errors.As(err, &ae) || err.Error() != "engine answered HTTP 500 to GET /version" {
		t.Fatalf("err = %.200q, want %q", err, "engine answered HTTP 500 to GET /version")
	}

	for _, level := range []string{"L1", "L2"} {
		fetch := func(ctx context.Context) (EngineFacts, error) { return fetchEngineVia(ctx, dial) }
		c := l2CheckWith(context.Background(), Options{DefaultLevel: level}, "linux", fetch)
		if strings.Contains(c.Detail, "pwn") || len(c.Detail) > 200 {
			t.Errorf("%s: detail carries the reason phrase (%d bytes): %.200q", level, len(c.Detail), c.Detail)
		}
		if !strings.Contains(c.Detail, "HTTP 500") {
			t.Errorf("%s: detail %q lacks the status code", level, c.Detail)
		}
	}
}
