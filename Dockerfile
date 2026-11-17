FROM golang:1.24-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/telenexus ./cmd/telenexus \
    && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY --from=build /out/telenexus /app/telenexus
COPY --from=build --chown=nonroot:nonroot /out/data /data
ENV HTTP_ADDR=:9487
ENV DATABASE_PATH=/data/telenexus.db
VOLUME ["/data"]
EXPOSE 9487
USER nonroot:nonroot
ENTRYPOINT ["/app/telenexus"]
