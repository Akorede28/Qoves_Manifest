// Stand-in implementation of the QOVES take-home API contract.
//
// The brief describes the app's contract; this
// file implements it exactly: GET / (hello), GET /healthz (SELECT 1 → 200/503),
// GET /metrics (Prometheus), connection string from DATABASE_URL only.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	httpRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total HTTP requests by path and status code.",
	}, []string{"path", "code"})

	httpDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request latency by path.",
		Buckets: prometheus.DefBuckets,
	}, []string{"path"})

	dbUp = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "app_database_up",
		Help: "1 if the last database health check succeeded, 0 otherwise.",
	})
)

func instrument(path string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		h(rec, r)
		httpDuration.WithLabelValues(path).Observe(time.Since(start).Seconds())
		httpRequests.WithLabelValues(path, http.StatusText(rec.status)).Inc()
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is not set; refusing to start (nothing is hard-coded by design)")
	}

	// Lazy pool: the app starts even if the DB is not yet ready, and
	// /healthz reports 503 until SELECT 1 succeeds. This avoids a startup
	// ordering dependency between the API and Postgres.
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		log.Fatalf("invalid DATABASE_URL: %v", err)
	}
	cfg.MaxConns = 5
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		log.Fatalf("creating connection pool: %v", err)
	}
	defer pool.Close()

	mux := http.NewServeMux()

	mux.HandleFunc("/", instrument("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("hello from the qoves take-home api\n"))
	}))

	mux.HandleFunc("/healthz", instrument("/healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		var one int
		if err := pool.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil {
			dbUp.Set(0)
			log.Printf("healthz: database unreachable: %v", err)
			http.Error(w, "database unreachable", http.StatusServiceUnavailable)
			return
		}
		dbUp.Set(1)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok\n"))
	}))

	mux.Handle("/metrics", promhttp.Handler())

	srv := &http.Server{
		Addr:              ":8080",
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Graceful shutdown so rollouts and HPA scale-downs do not drop requests.
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	log.Println("listening on :8080")
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server error: %v", err)
	}
}
