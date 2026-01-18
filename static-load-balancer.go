package main
import (
	"context"
	"crypto/tls"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)
type backend struct {
	URL    *url.URL
	Alive  int32
	Weight int
}
type pool struct {
	backends []*backend
	idx      uint32
	mu       sync.RWMutex
}
func newPool(raw []string) *pool {
	ps := &pool{}
	for _, r := range raw {
		u, _ := url.Parse(r)
		ps.backends = append(ps.backends, &backend{URL: u, Alive: 1, Weight: 1})
	}
	return ps
}
func (p *pool) next() *backend {
	n := len(p.backends)
	if n == 0 {
		return nil
	}
	i := int(atomic.AddUint32(&p.idx, 1)) % n
	for j := 0; j < n; j++ {
		b := p.backends[(i+j)%n]
		if atomic.LoadInt32(&b.Alive) == 1 {
			return b
		}
	}
	return p.backends[i]
}
func (p *pool) randomAlive() *backend {
	alive := make([]*backend, 0, len(p.backends))
	for _, b := range p.backends {
		if atomic.LoadInt32(&b.Alive) == 1 {
			for k := 0; k < b.Weight; k++ {
				alive = append(alive, b)
			}
		}
	}
	if len(alive) == 0 {
		return p.next()
	}
	return alive[rand.Intn(len(alive))]
}
func (p *pool) setAlive(u *url.URL, ok bool) {
	for _, b := range p.backends {
		if b.URL.Host == u.Host {
			if ok {
				atomic.StoreInt32(&b.Alive, 1)
			} else {
				atomic.StoreInt32(&b.Alive, 0)
			}
			return
		}
	}
}
func dialTimeout(addr string, to time.Duration) bool {
	d := net.Dialer{Timeout: to}
	c, err := d.Dial("tcp", addr)
	if err != nil {
		return false
	}
	c.Close()
	return true
}
func healthcheck(ctx context.Context, p *pool, interval time.Duration, timeout time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, b := range p.backends {
				ok := dialTimeout(b.URL.Host, timeout)
				p.setAlive(b.URL, ok)
			}
		}
	}
}
func proxyFor(b *backend) *httputil.ReverseProxy {
	director := func(r *http.Request) {
		r.URL.Scheme = b.URL.Scheme
		r.URL.Host = b.URL.Host
		r.Host = b.URL.Host
	}
	tr := &http.Transport{
		MaxIdleConns:        256,
		MaxIdleConnsPerHost: 64,
		IdleConnTimeout:     90 * time.Second,
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
		DisableCompression:  false,
	}
	rp := &httputil.ReverseProxy{Director: director, Transport: tr}
	return rp
}
func handler(p *pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b *backend
		if rand.Intn(10) < 3 {
			b = p.randomAlive()
		} else {
			b = p.next()
		}
		if b == nil {
			http.Error(w, "no backend", http.StatusServiceUnavailable)
			return
		}
		proxy := proxyFor(b)
		r.Context()
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, e error) {
			nb := p.randomAlive()
			if nb == nil || nb.URL.Host == b.URL.Host {
				http.Error(w, "backend error", http.StatusBadGateway)
				return
			}
			proxyFor(nb).ServeHTTP(w, r)
		}
		proxy.ServeHTTP(w, r)
	}
}
func startMock(addr, name string) {
	s := http.NewServeMux()
	s.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Duration(rand.Intn(40)) * time.Millisecond)
		w.Write([]byte(name))
	})
	go http.ListenAndServe(addr, s)
}
