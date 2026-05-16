package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
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
	activeOrders    metric.Int64UpDownCounter
	orderCount      int64
)

type OrderRequest struct {
	OrderID   string `json:"order_id"`
	ProductID string `json:"product_id"`
	Quantity  int    `json:"quantity"`
}

func main() {
	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "order-service"
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
	activeOrders, _ = meter.Int64UpDownCounter("active_orders", metric.WithDescription("Number of orders currently being processed"))

	http.HandleFunc("/order", handleOrder)
	http.HandleFunc("/health", handleHealth)

	srv := &http.Server{Addr: ":8001"}
	go func() {
		log.Printf("Starting order-service on :8001")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %s\n", err)
		}
	}()

	<-ctx.Done()
	log.Println("Shutting down order-service...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}
}

func handleOrder(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
	ctx, span := tracer.Start(ctx, "order-service.handleOrder")
	defer span.End()

	logger := global.Logger("order-service")
	activeOrders.Add(ctx, 1)
	defer activeOrders.Add(ctx, -1)

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		recordMetrics(ctx, "/order", r.Method, http.StatusMethodNotAllowed, start)
		return
	}

	var req OrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		recordMetrics(ctx, "/order", r.Method, http.StatusBadRequest, start)
		return
	}

	if req.OrderID == "" {
		req.OrderID = fmt.Sprintf("ORD-%d", atomic.AddInt64(&orderCount, 1))
	}

	span.SetAttributes(
		attribute.String("order_id", req.OrderID),
		attribute.String("product_id", req.ProductID),
		attribute.Int("quantity", req.Quantity),
	)

	var record otellog.Record
	record.SetBody(otellog.StringValue("Processing order"))
	record.AddAttributes(
		otellog.String("order_id", req.OrderID),
		otellog.String("product_id", req.ProductID),
	)
	logger.Emit(ctx, record)

	// 1. Call inventory-service
	if err := callInventory(ctx, req); err != nil {
		span.SetStatus(codes.Error, err.Error())
		http.Error(w, "Inventory check failed: "+err.Error(), http.StatusConflict)
		recordMetrics(ctx, "/order", r.Method, http.StatusConflict, start)
		return
	}

	// 2. Call notification-service
	if err := callNotification(ctx, req); err != nil {
		var record otellog.Record
		record.SetBody(otellog.StringValue("Notification failed"))
		record.AddAttributes(otellog.String("error", err.Error()))
		logger.Emit(ctx, record)
		// We might still consider the order created even if notification fails
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "Order Created", "order_id": req.OrderID})
	recordMetrics(ctx, "/order", r.Method, http.StatusOK, start)
}

func callInventory(ctx context.Context, req OrderRequest) error {
	ctx, span := tracer.Start(ctx, "order-service.callInventory")
	defer span.End()

	inventoryURL := os.Getenv("INVENTORY_SERVICE_URL")
	if inventoryURL == "" {
		inventoryURL = "http://inventory-service:8002"
	}

	body, _ := json.Marshal(req)
	hreq, _ := http.NewRequestWithContext(ctx, "POST", inventoryURL+"/reserve", bytes.NewBuffer(body))
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(hreq.Header))

	client := &http.Client{}
	resp, err := client.Do(hreq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("inventory returned status %d", resp.StatusCode)
	}
	return nil
}

func callNotification(ctx context.Context, req OrderRequest) error {
	ctx, span := tracer.Start(ctx, "order-service.callNotification")
	defer span.End()

	notificationURL := os.Getenv("NOTIFICATION_SERVICE_URL")
	if notificationURL == "" {
		notificationURL = "http://notification-service:8003"
	}

	body, _ := json.Marshal(req)
	hreq, _ := http.NewRequestWithContext(ctx, "POST", notificationURL+"/notify", bytes.NewBuffer(body))
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(hreq.Header))

	client := &http.Client{}
	resp, err := client.Do(hreq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
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
