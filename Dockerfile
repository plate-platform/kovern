FROM golang:1.27-alpine AS builder
ENV GOTOOLCHAIN=local
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a -o kovern ./cmd/main.go

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /build/kovern .
USER 65534:65534
ENTRYPOINT ["/kovern"]
