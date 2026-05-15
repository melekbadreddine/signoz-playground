# SigNoz Playground

A simple microservices application in Go to demonstrate manual OpenTelemetry instrumentation with SigNoz.

## Architecture

```text
[ Client ] -> [ api-gateway:8000 ]
                    |
                    v
            [ order-service:8001 ]
               /            \
              v              v
 [ inventory-service:8002 ] [ notification-service:8003 ]
```

All services export Traces, Metrics, and Logs via OTLP/gRPC to the SigNoz OTel Collector.

## Prerequisites

- Docker and Docker Compose
- SigNoz (running locally)

### Running SigNoz

The recommended way is to use the standard Docker Compose setup:
```bash
git clone -b main https://github.com/SigNoz/signoz.git
cd signoz/deploy/docker
docker compose up -d
```
This will start SigNoz and its OTel collector. By default, it creates a docker network called `clickhouse-setup_default`.

## Getting Started

1. **Check the SigNoz network name**:
   Run `docker network ls` to find the network created by SigNoz (likely `clickhouse-setup_default`).
   
2. **Update `docker-compose.yml`**:
   If the network name is different from `observability`, update the `external: true` name at the bottom of `docker-compose.yml`.

3. **Build and run the application**:
   ```bash
   docker compose up --build
   ```
   This will start all services PLUS a **load generator** that sends a request every few seconds.

4. **Trigger a manual trace (Optional)**:
   ```bash
   curl -X POST http://localhost:8000/order \
     -H "Content-Type: application/json" \
     -d '{"product_id": "product-1", "quantity": 2}'
   ```

## Load Generator

The `loadgen` service (Python) is included in the Docker Compose. It:
- Waits for services to start.
- Randomly picks a product and quantity.
- Sends a POST request to `api-gateway` every 1-5 seconds.
- Logs successes and failures to the console.

## What to look for in SigNoz

1. **Service Graph**: See the relationship between all 4 services.
2. **Traces**: Go to the "Traces" tab. You should see a trace for `/order` spanning all 4 services.
3. **Metrics**:
   - `http_requests_total`: Monitor request counts.
   - `http_request_duration_ms`: Analyze latency.
   - `active_orders`: Gauge in `order-service`.
   - `stock_level`: Gauge in `inventory-service` (per product).
4. **Logs**:
   - Go to "Logs" tab.
   - You should see structured logs like "Processing order", "Stock reserved", etc.
   - Click on a log to see its associated `trace_id` and `span_id`.
   - Click "View Trace" from a log entry to jump to the waterfall view.

## Instrumentation Details

- **Traces**: Manual span creation using `tracer.Start()`. Context propagation using W3C TraceContext.
- **Metrics**: OTel Go metrics API using `MeterProvider`. Periodic OTLP export.
- **Logs**: OTel Go logs bridge. Each log entry is correlated with the current span context.
