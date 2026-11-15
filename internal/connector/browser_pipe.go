package connector

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"log/slog"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/coder/websocket"
)

// maxCDPMessage bounds one CDP message either way (screencast frames are
// JPEG base64; a screenshot of a big page is a few MB).
const maxCDPMessage = 32 << 20

func basicAuth(userinfo string) string {
	return base64.StdEncoding.EncodeToString([]byte(userinfo))
}

// pipeCDP connects to the browser's WebSocket and copies lines from in to
// it and its messages (one per line) to out, until either side ends.
func pipeCDP(ctx context.Context, wsURL string, in io.Reader, out io.Writer) error {
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return errx.Wrap(err, "connect to the browser", errx.TypeBusiness)
	}
	ws.SetReadLimit(maxCDPMessage)
	defer ws.Close(websocket.StatusNormalClosure, "")
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if _, err := io.WriteString(out, hosting.BrowserHello+"\n"); err != nil {
		return nil
	}
	errs := make(chan error, 2)
	go func() {
		sc := bufio.NewScanner(in)
		sc.Buffer(make([]byte, 64<<10), maxCDPMessage)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			if err := ws.Write(ctx, websocket.MessageText, line); err != nil {
				errs <- err
				return
			}
		}
		errs <- sc.Err() // nil: stdin closed
	}()
	go func() {
		w := bufio.NewWriterSize(out, 64<<10)
		for {
			_, msg, err := ws.Read(ctx)
			if err != nil {
				errs <- err
				return
			}
			if _, err := w.Write(msg); err != nil {
				errs <- err
				return
			}
			if err := w.WriteByte('\n'); err != nil {
				errs <- err
				return
			}
			if err := w.Flush(); err != nil {
				errs <- err
				return
			}
		}
	}()
	err = <-errs
	cancel()
	if err != nil && ctx.Err() == nil {
		slog.Debug("browser relay ended", "err", err)
	}
	return nil
}
