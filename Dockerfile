FROM golang:1.27.1-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux \
    go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/local-inference-router \
    ./cmd/local-inference-router

FROM gcr.io/distroless/static-debian13:nonroot

COPY --from=build \
    /out/local-inference-router \
    /local-inference-router

USER 65532:65532

ENTRYPOINT ["/local-inference-router"]
