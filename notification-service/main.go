package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

var (
	tracer          trace.Tracer
	meter           metric.Meter
	requestCounter  metric.Int64Counter
	requestDuration metric.Float64Histogram
)

type OrderRequest struct {
	OrderID   string `json:"order_id"`
	ProductID string `json:"product_id"`
	Quantity  int    `json:"quantity"`
}

func main() {
	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "notification-service"
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdown, err := initOTel(ctx, serviceName)
	if err != nil {
		log.Fatalf("failed to initialize OTel: %v", err)
	}
	defer func() {
		if err := shutdown(context.Background()); err != nil {
			log.Printf("failed to shutdown OTel: %v", err)
		}
	}()

	tracer = otel.Tracer(serviceName)
	meter = otel.Meter(serviceName)

	requestCounter, _ = meter.Int64Counter("http_requests_total")
	requestDuration, _ = meter.Float64Histogram("http_request_duration_ms")

	http.HandleFunc("/notify", handleNotify)
	http.HandleFunc("/health", handleHealth)

	srv := &http.Server{Addr: ":8003"}
	go func() {
		log.Printf("Starting notification-service on :8003")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %s\n", err)
		}
	}()

	<-ctx.Done()
	log.Println("Shutting down notification-service...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}
}

func handleNotify(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
	ctx, span := tracer.Start(ctx, "notification-service.handleNotify")
	defer span.End()

	logger := global.Logger("notification-service")

	var req OrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		recordMetrics(ctx, "/notify", r.Method, http.StatusBadRequest, start)
		return
	}

	// Simulate latency
	time.Sleep(100 * time.Millisecond)

	logger.Emit(ctx, "Notification sent",
		attribute.String("order_id", req.OrderID),
		attribute.String("product_id", req.ProductID),
	)

	w.WriteHeader(http.StatusOK)
	recordMetrics(ctx, "/notify", r.Method, http.StatusOK, start)
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func recordMetrics(ctx context.Context, route, method string, statusCode int, start time.Now) {
	duration := float64(time.Since(start).Milliseconds())
	attrs := []attribute.KeyValue{
		attribute.String("route", route),
		attribute.String("method", method),
		attribute.Int("status_code", statusCode),
	}
	requestCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
	requestDuration.Record(ctx, duration, metric.WithAttributes(attrs...))
}
