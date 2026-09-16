package bot

import (
	"context"
	"crypto/tls"
	"errors"
	"log"
	"net"
	"net/url"
	"strconv"
	"time"
)

var errNoCertificate = errors.New("no certificate presented")

// Certificate expiry.
//
// Caddy renews automatically, which is exactly why this is worth watching: a
// certificate getting close to its expiry does not mean "time to renew", it
// means renewal has stopped working, and that is a silent failure until the
// browser shows a warning to everyone at once.
//
// Checked twice a day. The de-duplication key includes the day and the
// threshold, so each warning is said once per day per host rather than on
// every cycle.

const (
	certInterval = 12 * time.Hour
	certTimeout  = 10 * time.Second
)

// Thresholds in days. Three messages, spaced so the first is easy to ignore
// and the last is not.
var certThresholds = []int{14, 7, 3}

func (b *Bot) runCertWatch(ctx context.Context) {
	ticker := time.NewTicker(certInterval)
	defer ticker.Stop()

	b.checkCertificates(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.checkCertificates(ctx)
		}
	}
}

func (b *Bot) checkCertificates(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()

	for _, raw := range []string{b.cfg.PublicSiteURL, b.cfg.AdminSiteURL, b.cfg.PublicAPIURL} {
		host := hostOf(raw)
		if host == "" {
			continue
		}

		expiry, err := certExpiry(cctx, host)
		if err != nil {
			// Unreachable is the health watcher's job to report, not this
			// one's: two messages about one outage is one too many.
			log.Printf("bot: certificate check failed for %s: %v", host, err)
			continue
		}

		days := int(time.Until(expiry).Hours() / 24)
		threshold, worth := certThresholdFor(days)
		if !worth {
			continue
		}

		b.queueScheduled(cctx, "cert.expiring", map[string]any{
			"host":  host,
			"days":  days,
			"until": expiry.In(tashkent).Format("02.01.2006"),
		}, sevCritical,
			// One warning per host per threshold per day.
			"cert:"+host+":"+strconv.Itoa(threshold)+":"+time.Now().In(tashkent).Format("2006-01-02"),
			time.Time{})
	}
}

// certThresholdFor returns the tightest threshold the remaining days have
// crossed. Below zero is past expiry, which is still worth saying out loud.
//
// Walked backwards because certThresholds is written loosest-first, for
// reading: three days left crosses all three thresholds, and the one that
// describes the situation is 3, not 14.
func certThresholdFor(days int) (int, bool) {
	for i := len(certThresholds) - 1; i >= 0; i-- {
		if days <= certThresholds[i] {
			return certThresholds[i], true
		}
	}
	return 0, false
}

func certExpiry(ctx context.Context, host string) (time.Time, error) {
	// tls.Dialer rather than tls.DialWithDialer: this one honours the context,
	// so a hung handshake cannot outlive the round it belongs to.
	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: certTimeout}}

	conn, err := dialer.DialContext(ctx, "tcp", host+":443")
	if err != nil {
		return time.Time{}, err
	}
	defer conn.Close()

	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return time.Time{}, errNoCertificate
	}

	chain := tlsConn.ConnectionState().PeerCertificates
	if len(chain) == 0 {
		return time.Time{}, errNoCertificate
	}
	// The leaf is first; the rest of the chain expires later by construction.
	return chain[0].NotAfter, nil
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Hostname()
}
