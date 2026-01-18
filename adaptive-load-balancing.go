package main
import (
	"context"
	"log"
	"math"
	"math/rand"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"time"
)
type backend struct{ u *url.URL; alive int32; rtt int64 }
var pool []*backend
var rr uint32
func newBackend(raw string)*backend{u,_:=url.Parse(raw);return &backend{u: u, alive:1, rtt: 50}}
func pick()*backend{
	n:=len(pool); if n==0{ return nil }
	best:=(*backend)(nil); brtt:=int64(math.MaxInt64)
	start:=int(atomic.AddUint32(&rr,1))%n
	for i:=0;i<n;i++{ b:=pool[(start+i)%n]; if atomic.LoadInt32(&b.alive)==1 && atomic.LoadInt64(&b.rtt)<=brtt{ best=b; brtt=atomic.LoadInt64(&b.rtt) } }
	if best==nil{ return pool[start] }
	return best
}
func aliveCount()int{ c:=0; for _,b:=range pool{ if atomic.LoadInt32(&b.alive)==1{ c++ } }; return c }
func proxyFor(b *backend)*httputil.ReverseProxy{
	d:=func(r *http.Request){ r.URL.Scheme=b.u.Scheme; r.URL.Host=b.u.Host; r.Host=b.u.Host }
	return &httputil.ReverseProxy{Director:d}
}
func handler(w http.ResponseWriter,r *http.Request){
	b:=pick(); if b==nil{ http.Error(w,"no backend",503); return }
	p:=proxyFor(b)
	p.ErrorHandler=func(w http.ResponseWriter,r *http.Request,e error){ nb:=pick(); if nb==nil||nb==b{ http.Error(w,"unavailable",502); return }; proxyFor(nb).ServeHTTP(w,r) }
	p.ServeHTTP(w,r)
}
func probe(ctx context.Context,b *backend,interval time.Duration){
	c:=http.Client{Timeout:500*time.Millisecond}
	t:=time.NewTicker(interval)
	for{
		select{
		case <-ctx.Done(): return
		case <-t.C:
			st:=time.Now()
			resp,err:=c.Get(b.u.String()+"/health")
			if err!=nil{ atomic.StoreInt32(&b.alive,0); atomic.StoreInt64(&b.rtt,math.MaxInt64); continue }
			resp.Body.Close()
			atomic.StoreInt32(&b.alive,1)
			atomic.StoreInt64(&b.rtt,time.Since(st).Milliseconds()+int64(rand.Intn(10)))
		}
	}
}
func startMock(addr,name string){
	mux:=http.NewServeMux()
	mux.HandleFunc("/health",func(w http.ResponseWriter,r *http.Request){ w.WriteHeader(200) })
	mux.HandleFunc("/",func(w http.ResponseWriter,r *http.Request){ time.Sleep(time.Duration(50+rand.Intn(120))*time.Millisecond); w.Write([]byte(name)) })
	go http.ListenAndServe(addr,mux)
}
func main(){
	rand.Seed(time.Now().UnixNano())
	startMock(":8081","srv1"); startMock(":8082","srv2"); startMock(":8083","srv3")
	pool=[]*backend{ newBackend("http://localhost:8081"), newBackend("http://localhost:8082"), newBackend("http://localhost:8083") }
	ctx,cancel:=context.WithCancel(context.Background()); defer cancel()
	for _,b:=range pool{ go probe(ctx,b,700*time.Millisecond) }
	go func(){ t:=time.NewTicker(3*time.Second); for range t.C{ log.Printf("alive:%d rtts:%d/%d/%d", aliveCount(), atomic.LoadInt64(&pool[0].rtt), atomic.LoadInt64(&pool[1].rtt), atomic.LoadInt64(&pool[2].rtt)) } }()
	srv:=&http.Server{Addr:":8080",Handler:http.HandlerFunc(handler),ReadTimeout:3*time.Second,WriteTimeout:15*time.Second}
	log.Println("adaptive lb on :8080")
	log.Fatal(srv.ListenAndServe())
}
func _unused() {}
