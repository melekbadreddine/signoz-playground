package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
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

func main() {
	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "api-gateway"
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

	requestCounter, _ = meter.Int64Counter("http_requests_total", metric.WithDescription("Total number of HTTP requests"))
	requestDuration, _ = meter.Float64Histogram("http_request_duration_ms", metric.WithDescription("HTTP request duration in milliseconds"))

	http.HandleFunc("/order", handleOrder)
	http.HandleFunc("/health", handleHealth)

	srv := &http.Server{Addr: ":8000"}
	go func() {
		log.Printf("Starting api-gateway on :8000")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %s\n", err)
		}
	}()

	<-ctx.Done()
	log.Println("Shutting down api-gateway...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}
}

func handleOrder(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
	ctx, span := tracer.Start(ctx, "api-gateway.handleOrder", trace.WithAttributes(
		attribute.String("http.method", r.Method),
		attribute.String("http.route", "/order"),
	))
	defer span.End()

	logger := global.Logger("api-gateway")

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		recordMetrics(ctx, "/order", r.Method, http.StatusMethodNotAllowed, start)
		return
	}

	body, _ := io.ReadAll(r.Body)
	// Log the incoming request
	logger.Emit(ctx, "Order request received", attribute.String("body", string(body)))

	// Forward to order-service
	orderServiceURL := os.Getenv("ORDER_SERVICE_URL")
	if orderServiceURL == "" {
		orderServiceURL = "http://order-service:8001"
	}

	req, err := http.NewRequestWithContext(ctx, "POST", orderServiceURL+"/order", bytes.NewBuffer(body))
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		recordMetrics(ctx, "/order", r.Method, http.StatusInternalServerError, start)
		return
	}

	// Propagate context
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		recordMetrics(ctx, "/order", r.Method, http.StatusInternalServerError, start)
		return
	}
	defer resp.Body.Close()

	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)

	recordMetrics(ctx, "/order", r.Method, resp.StatusCode, start)
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
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
