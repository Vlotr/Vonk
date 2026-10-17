package ingest

import (
	"context"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"monitor/internal/posthog"
	"monitor/internal/store/storetest"
)

// While the database cannot be reached, a batch is kept and tried again
// with backoff, longer than the retries of a refused write; it is dropped
// only when that lasts too long, or sooner once the writer stops.
func TestWriter_KeepsABatchWhileTheDatabaseIsUnreachable(t *testing.T) {
	env := newTestEnv(t, 100)
	w := NewWriter(env.db, env.queue, &recordingHooks{}, env.logger)
	w.retryDelays = []time.Duration{time.Millisecond}
	w.unavailableWait, w.unavailableMaxWait = time.Millisecond, 4*time.Millisecond

	unreachable := []error{
		&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
		&pgconn.PgError{Code: "57P03", Message: "the database system is starting up"},
		&pgconn.PgError{Code: "57P01", Message: "terminating connection due to administrator command"},
		io.ErrUnexpectedEOF,
	}
	calls := 0
	ok := w.save(3, time.Now().Add(w.unavailableFor), func() error {
		calls++
		if calls <= 10 {
			return unreachable[calls%len(unreachable)]
		}
		return nil
	})
	if !ok || calls != 11 || w.Stats().LostEvents != 0 {
		t.Errorf("save = %v after %d calls, stats %+v; want stored after 10 failures", ok, calls, w.Stats())
	}
	if strings.Contains(env.logs.String(), "level=ERROR") || !strings.Contains(env.logs.String(), "cannot be reached") {
		t.Errorf("logs:\n%s", env.logs.String())
	}

	// Unreachable for longer than it is kept: dropped and counted.
	w.unavailableFor = 20 * time.Millisecond
	start := time.Now()
	if w.save(2, time.Now().Add(w.unavailableFor), func() error { return unreachable[0] }) || w.Stats().LostEvents != 2 {
		t.Errorf("an outage longer than kept: stats %+v, want 2 lost", w.Stats())
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("gave up after %v", took)
	}

	// Once the writer stops, it waits at most stopGrace for the database.
	w.unavailableFor, w.stopGrace = time.Hour, 30*time.Millisecond
	w.Close()
	start = time.Now()
	w.save(1, time.Now().Add(w.unavailableFor), func() error { return unreachable[0] })
	if took := time.Since(start); took > time.Second {
		t.Errorf("a stopping writer waited %v for the database", took)
	}
}

// The saves of one flush (errors, page events, custom events) share one
// window for an unreachable database: a batch of all three kinds waits at
// most that long, not three times as long.
func TestWriter_OutageWindowIsPerFlush(t *testing.T) {
	if !storetest.Postgres() {
		t.Skip("needs Postgres: make test-postgres")
	}
	u, err := url.Parse(storetest.DatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	proxy := startOutageProxy(t, u.Host)
	u.Host = proxy.ln.Addr().String()
	env := newTestEnvAt(t, 100, u.String())
	p := env.project(t, "Webshop", 0)
	w := NewWriter(env.db, env.queue, &recordingHooks{}, env.logger)
	w.unavailableFor = 800 * time.Millisecond
	w.unavailableWait, w.unavailableMaxWait = 10*time.Millisecond, 20*time.Millisecond

	proxy.setDown(true)
	batch := []Item{
		errorItem(t, p.ID, eventIDN(1), "charge", t0, t0),
		{ProjectID: p.ID, ReceivedAt: t0, Route: "/", Path: "/", Page: &posthog.Event{UUID: "pv-1", Name: posthog.EventPageview, DistinctID: "42"}},
		{ProjectID: p.ID, ReceivedAt: t0, Route: "/", Path: "/", Custom: &posthog.Event{UUID: "ce-1", Name: "checkout_started", DistinctID: "42"}},
	}
	start := time.Now()
	w.flush(context.Background(), batch)
	took := time.Since(start)
	if took < 800*time.Millisecond || took > 1600*time.Millisecond {
		t.Errorf("the flush waited %v for the database, want about 0.8 s (one window), not 2.4 s (one per kind)", took)
	}
	if st := w.Stats(); st.LostEvents != 3 || st.FailedBatches != 3 {
		t.Errorf("stats = %+v, want the three parts counted as lost", st)
	}
}

// outageProxy stands between the tests and Postgres and can go down: it
// cuts every connection and refuses new ones, as a restarting Postgres
// does, until it comes back.
type outageProxy struct {
	ln     net.Listener
	target string

	mu    sync.Mutex
	down  bool
	conns []net.Conn
}

func startOutageProxy(t *testing.T, target string) *outageProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &outageProxy{ln: ln, target: target}
	go p.serve()
	t.Cleanup(func() {
		ln.Close()
		p.setDown(true)
	})
	return p
}

func (p *outageProxy) serve() {
	for {
		client, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		server, err := net.Dial("tcp", p.target)
		if p.down || err != nil {
			p.mu.Unlock()
			client.Close()
			if server != nil {
				server.Close()
			}
			continue
		}
		p.conns = append(p.conns, client, server)
		p.mu.Unlock()
		go func() { io.Copy(server, client); server.Close() }()
		go func() { io.Copy(client, server); client.Close() }()
	}
}

func (p *outageProxy) setDown(down bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.down = down
	if down {
		for _, c := range p.conns {
			c.Close()
		}
		p.conns = nil
	}
}

// Postgres goes away for 10 seconds (a restart, a failover), longer than
// the retries of a refused write: the events ingest accepted meanwhile are
// all stored once it is back, none lost. The database check fails during
// the outage, so /health and the status page say so.
func TestWriter_NoEventLostWhilePostgresRestarts(t *testing.T) {
	if !storetest.Postgres() {
		t.Skip("needs Postgres: make test-postgres")
	}
	u, err := url.Parse(storetest.DatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	proxy := startOutageProxy(t, u.Host)
	u.Host = proxy.ln.Addr().String()
	env := newTestEnvAt(t, 100, u.String())
	p := env.project(t, "Webshop", 0)
	w := startWriter(t, env, &recordingHooks{}, func(w *Writer) { w.flushInterval = 20 * time.Millisecond })
	ctx := context.Background()

	enqueue(t, env, errorItem(t, p.ID, eventIDN(1), "charge", t0, t0))
	eventually(t, "the first event", func() bool { n, _ := env.db.CountErrorEvents(ctx, p.ID); return n == 1 })

	proxy.setDown(true)
	for i := 2; i <= 21; i++ {
		enqueue(t, env, errorItem(t, p.ID, eventIDN(i), "charge", t0.Add(time.Duration(i)*time.Second), t0))
	}
	time.Sleep(10 * time.Second)
	if err := env.db.Ping(ctx); err == nil {
		t.Error("the database check passes while Postgres is away")
	}
	proxy.setDown(false)
	w.Close()

	if n, err := env.db.CountErrorEvents(ctx, p.ID); err != nil || n != 21 {
		t.Errorf("stored %d events (%v), want all 21", n, err)
	}
	if st := w.Stats(); st.LostEvents != 0 || st.FailedBatches != 0 {
		t.Errorf("stats = %+v, want nothing lost", st)
	}
}
