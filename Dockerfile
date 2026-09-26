# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM node:24-bookworm-slim AS frontend
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# Build on the target architecture. The MCR evaluator uses CGO/C++, so a
# GOARCH-only cross compile would be incorrect without a matching C++ toolchain.
FROM golang:1.27.1-bookworm AS backend
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /src/web/dist ./web/dist
RUN CGO_ENABLED=1 go build -trimpath -ldflags='-s -w' -o /out/openmajiang ./cmd/openmajiang \
    && CGO_ENABLED=1 go build -trimpath -ldflags='-s -w' -o /out/bot-runner ./cmd/bot-runner

FROM debian:bookworm-slim AS runtime
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates libstdc++6 \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 openmajiang \
    && useradd --uid 10001 --gid 10001 --no-create-home --shell /usr/sbin/nologin openmajiang
COPY --from=backend /out/ /usr/local/bin/
WORKDIR /app
USER 10001:10001
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 \
    CMD ["openmajiang", "healthcheck"]
ENTRYPOINT ["openmajiang"]
CMD ["serve"]
