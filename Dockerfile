FROM golang:1.21 AS build

RUN apt-get update && apt-get install -y \
  ca-certificates \
  tzdata \
  curl

WORKDIR /usr/src/app

# Install pdfium
RUN curl -L https://github.com/bblanchon/pdfium-binaries/releases/download/chromium%2F6015/pdfium-linux-x64.tgz -o pdfium-linux-x64.tgz && \
    mkdir /opt/pdfium && \
    tar -C /opt/pdfium -xvf pdfium-linux-x64.tgz

# Copy in go.mud/sum first to be able to cache that step first.
COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .

RUN go build -o bin/api main.go

COPY pdfium.pc /usr/lib/pkgconfig/pdfium.pc
RUN go build -o bin/pdfium worker/main.go

FROM debian:bookworm

COPY pdfium.pc /usr/lib/pkgconfig/pdfium.pc

COPY --from=build /usr/src/app/bin /app
COPY --from=build /opt/pdfium /opt/pdfium

ENV PDFIUM_WORKER=/app/pdfium
ENV ENVIRONMENT=production
CMD ["/app/api"]