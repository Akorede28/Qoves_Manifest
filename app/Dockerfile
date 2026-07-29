# --- Build stage -------------------------------------------------------------
# Pinned minor version; in the writeup we note that production would pin the
# base images by digest as well (the app image itself is deployed by digest).
FROM golang:1.23-alpine AS build

WORKDIR /src

# Cache dependency downloads separately from source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY main.go ./

# Static binary: no libc dependency, so the runtime image can be distroless.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api .

# --- Runtime stage -----------------------------------------------------------
# distroless/static: no shell, no package manager, tiny attack surface.
# Tradeoff (worth defending in the walkthrough): no shell means no exec-ing
# into the container to debug; you debug via logs, /metrics, and ephemeral
# debug containers (kubectl debug).
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/api /api

USER nonroot:nonroot
EXPOSE 8080

ENTRYPOINT ["/api"]
