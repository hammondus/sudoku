# Multi-stage build. Pure Go (modernc.org/sqlite, no CGO), so the binary is
# static and runs on distroless "static": no libc, no shell. web/ and VERSION
# are embedded at build time, so the image is one binary plus an empty /data.
#
# `make deploy` builds on the deploy host (linux/arm64), so `go build` targets
# the right architecture with no cross-compile flags here.

FROM golang:1.27 AS build
WORKDIR /src

# Modules first: the download layer is cached until go.mod or go.sum changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/sudoku .
RUN mkdir -m 0700 /out/data

FROM gcr.io/distroless/static-debian13:nonroot
WORKDIR /app
COPY --from=build /out/sudoku /app/sudoku

# /data must exist with nonroot ownership: a fresh named volume copies the
# ownership of the directory it mounts over. Without this the volume starts
# root-owned and the first start can't create the database.
COPY --from=build --chown=nonroot:nonroot /out/data /data

# The default for -db, and what `docker compose exec sudoku /app/sudoku
# -invite ...` uses, so account commands need no -db flag.
ENV SUDOKU_DB=/data/sudoku.db

EXPOSE 8080

# Distroless has no shell or curl; the binary probes its own /healthz.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD ["/app/sudoku", "-healthcheck", "-addr", ":8080"]

ENTRYPOINT ["/app/sudoku"]
CMD ["-addr", ":8080"]
