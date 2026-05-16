package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
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
	stockLevel      metric.Int64ObservableGauge
	inventory       = map[string]int64{
		"product-1": 100,
		"product-2": 50,
		"product-3": 10,
	}
	mu sync.Mutex
)

type OrderRequest struct {
	OrderID   string `json:"order_id"`
	ProductID string `json:"product_id"`
	Quantity  int    `json:"quantity"`
}

func main() {
	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "inventory-service"
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
	
	_, _ = meter.Int64ObservableGauge("stock_level", metric.WithInt64Callback(func(ctx context.Context, observer metric.Int64Observer) error {
		mu.Lock()
		defer mu.Unlock()
		for pid, level := range inventory {
			observer.Observe(level, metric.WithAttributes(attribute.String("product_id", pid)))
		}
		return nil
	}))

	http.HandleFunc("/reserve", handleReserve)
	http.HandleFunc("/health", handleHealth)

	srv := &http.Server{Addr: ":8002"}
	go func() {
		log.Printf("Starting inventory-service on :8002")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %s\n", err)
		}
	}()

	<-ctx.Done()
	log.Println("Shutting down inventory-service...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}
}

func handleReserve(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
	ctx, span := tracer.Start(ctx, "inventory-service.handleReserve")
	defer span.End()

	logger := global.Logger("inventory-service")

	var req OrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		recordMetrics(ctx, "/reserve", r.Method, http.StatusBadRequest, start)
		return
	}

	mu.Lock()
	level, ok := inventory[req.ProductID]
	if !ok {
		mu.Unlock()
		http.Error(w, "Product not found", http.StatusNotFound)
		recordMetrics(ctx, "/reserve", r.Method, http.StatusNotFound, start)
		return
	}

	if level < int64(req.Quantity) {
		mu.Unlock()
		http.Error(w, "Insufficient stock", http.StatusConflict)
		recordMetrics(ctx, "/reserve", r.Method, http.StatusConflict, start)
		return
	}

	inventory[req.ProductID] -= int64(req.Quantity)
	newLevel := inventory[req.ProductID]
	mu.Unlock()

	span.SetAttributes(
		attribute.String("product_id", req.ProductID),
		attribute.Int64("stock_level", newLevel),
	)

	var record otellog.Record
	record.SetBody(otellog.StringValue("Stock reserved"))
	record.AddAttributes(
		otellog.String("order_id", req.OrderID),
		otellog.String("product_id", req.ProductID),
		otellog.Int64("remaining_stock", newLevel),
	)
	logger.Emit(ctx, record)

	w.WriteHeader(http.StatusOK)
	recordMetrics(ctx, "/reserve", r.Method, http.StatusOK, start)
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func recordMetrics(ctx context.Context, route, method string, statusCode int, start time.Time) {
	duration := float64(time.Since(start).Milliseconds())
	attrs := []attribute.KeyValue{
		attribute.String("route", route),
		attribute.String("method", method),
		attribute.Int("status_code", statusCode),
	}
	requestCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
	requestDuration.Record(ctx, duration, metric.WithAttributes(attrs...))
}
